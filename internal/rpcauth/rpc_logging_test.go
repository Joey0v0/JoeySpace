package rpcauth

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// A subprocess isolates go-zero's global logger, metrics and shutdown hooks.
// The unprotected control must actually print synthetic request fields, so this
// verifies the real framework interceptor rather than only the config slice.
func TestRPCRequestContentPolicyOnRealGoZeroServer(t *testing.T) {
	for _, slow := range []string{"normal", "slow"} {
		for _, protected := range []string{"control", "protected"} {
			t.Run(slow+"/"+protected, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRPCRequestContentPolicyChild$")
				command.Env = append(os.Environ(), "GO_IM_RPC_LOG_TEST="+protected, "GO_IM_RPC_LOG_SPEED="+slow)
				output, err := command.CombinedOutput()
				if err != nil || !strings.Contains(string(output), "RPC_LOG_TEST_COMPLETE") {
					t.Fatalf("isolated RPC logging test failed: %v (completion=%t)", err, strings.Contains(string(output), "RPC_LOG_TEST_COMPLETE"))
				}
				for _, marker := range []string{"synthetic-password", "synthetic-group", "synthetic-title", "synthetic-question"} {
					found := strings.Contains(string(output), marker)
					if found != (protected == "control") {
						t.Errorf("request content visibility=%t for %s; mode=%s", found, marker, protected)
					}
				}
				if strings.Contains(string(output), "synthetic-bearer") {
					t.Error("RPC metadata appeared in framework logs")
				}
				if protected == "protected" && slow == "slow" && !strings.Contains(string(output), "slowcall") {
					t.Error("body suppression also removed slow-call timing logs")
				}
			})
		}
	}
}

func TestRPCRequestContentPolicyChild(t *testing.T) {
	mode := os.Getenv("GO_IM_RPC_LOG_TEST")
	if mode == "" {
		t.Skip("subprocess only")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var c zrpc.RpcServerConf
	if err := conf.LoadFromYamlBytes([]byte("Name: rpc-content-test\nListenOn: "+address+"\nMode: test\nLog:\n  Mode: console\n"), &c); err != nil {
		t.Fatal(err)
	}
	c.Middlewares.StatConf.SlowThreshold = time.Hour
	if os.Getenv("GO_IM_RPC_LOG_SPEED") == "slow" {
		c.Middlewares.StatConf.SlowThreshold = time.Nanosecond
	}
	if mode == "protected" {
		c = WithoutRPCRequestContent(c, &userpb.User_ServiceDesc, &impb.IM_ServiceDesc, &taskpb.Task_ServiceDesc, &agentpb.Agent_ServiceDesc)
	}
	ready := make(chan struct{})
	server, err := zrpc.NewServer(c, func(server *grpc.Server) {
		userpb.RegisterUserServer(server, &userpb.UnimplementedUserServer{})
		impb.RegisterIMServer(server, &impb.UnimplementedIMServer{})
		taskpb.RegisterTaskServer(server, &taskpb.UnimplementedTaskServer{})
		agentpb.RegisterAgentServer(server, &agentpb.UnimplementedAgentServer{})
		close(ready)
	})
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("GO_IM_RPC_LOG_SPEED") == "slow" {
		// Force a genuinely slow handler; go-zero's cached clock can measure
		// a fast Unimplemented response as zero, even with a 1ns threshold.
		server.AddUnaryInterceptors(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			timer := time.NewTimer(20 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, status.FromContextError(ctx.Err()).Err()
			case <-timer.C:
				return handler(ctx, req)
			}
		})
	}
	go server.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("go-zero RPC server did not start")
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer synthetic-bearer"))
	requests := []struct {
		method string
		value  any
	}{
		{userpb.User_Login_FullMethodName, &userpb.LoginRequest{Username: "synthetic-user", Password: "synthetic-password"}},
		{impb.IM_CreateTeamGroup_FullMethodName, &impb.CreateTeamGroupRequest{TeamId: 1, Name: "synthetic-group"}},
		{taskpb.Task_CreateTask_FullMethodName, &taskpb.CreateTaskRequest{TeamId: 1, Title: "synthetic-title", Description: "synthetic-description"}},
		{agentpb.Agent_Ask_FullMethodName, &agentpb.AskRequest{TeamId: 1, GroupId: 2, Question: "synthetic-question"}},
	}
	for _, request := range requests {
		// Unimplemented business handlers still pass through the real stat middleware.
		err := connection.Invoke(ctx, request.method, request.value, &userpb.LoginResponse{}, grpc.WaitForReady(true))
		if status.Code(err) != codes.Unimplemented {
			t.Fatalf("unexpected framework RPC outcome: %s", status.Code(err))
		}
	}
	_ = connection.Close()
	logx.Info("RPC_LOG_TEST_COMPLETE")
	logx.Close()
	// go-zero installs process-global shutdown hooks; this isolated process owns
	// its server/listener and exits after this sole test, without retaining them in the parent.
}
