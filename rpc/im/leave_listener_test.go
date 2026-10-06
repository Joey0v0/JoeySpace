package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func leaveListenerEnv() map[string]string {
	return map[string]string{
		"IM_LEAVE_LISTEN_ON":     "127.0.0.1:9008",
		"IM_LEAVE_TLS_CERT_FILE": "im.pem",
		"IM_LEAVE_TLS_KEY_FILE":  "im.key",
		"IM_LEAVE_TLS_CA_FILE":   "ca.pem",
	}
}

func TestIMLeaveConfigIsOptInAndPartialFailsClosed(t *testing.T) {
	if c, err := loadIMLeaveConfig(func(string) string { return "" }); err != nil || !c.disabled() {
		t.Fatalf("disabled config: %+v, %v", c, err)
	}
	if _, err := loadIMLeaveConfig(nil); err == nil {
		t.Fatal("nil environment reader accepted")
	}
	env := leaveListenerEnv()
	if c, err := loadIMLeaveConfig(func(key string) string { return env[key] }); err != nil || c.disabled() {
		t.Fatalf("complete config: %+v, %v", c, err)
	}
	for key := range env {
		values := leaveListenerEnv()
		values[key] = ""
		if _, err := loadIMLeaveConfig(func(k string) string { return values[k] }); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
		values[key] = " " + env[key]
		if _, err := loadIMLeaveConfig(func(k string) string { return values[k] }); err == nil {
			t.Fatalf("padded %s accepted", key)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "localhost", "localhost:65536", "*:9008", "localhost:9008 ", "127.0.0.1:-1"} {
		values := leaveListenerEnv()
		values["IM_LEAVE_LISTEN_ON"] = addr
		if _, err := loadIMLeaveConfig(func(k string) string { return values[k] }); err == nil {
			t.Fatalf("invalid address %q accepted", addr)
		}
	}
}

func TestIMLeaveStartupRejectsExistingPorts(t *testing.T) {
	c, err := loadIMLeaveConfig(func(k string) string { return leaveListenerEnv()[k] })
	if err != nil {
		t.Fatal(err)
	}
	for _, addresses := range [][3]string{
		{"127.0.0.1:9008", "", ""},
		{"127.0.0.1:9002", "0.0.0.0:9008", ""},
		{"127.0.0.1:9002", "", "[::1]:9008"},
		{"", "", ""},
	} {
		if err := validateIMLeaveStartup(c, addresses[0], addresses[1], addresses[2]); err == nil {
			t.Fatalf("accepted conflicting/invalid addresses: %v", addresses)
		}
	}
	if err := validateIMLeaveStartup(c, "127.0.0.1:9002", "0.0.0.0:9005", "127.0.0.1:9006"); err != nil {
		t.Fatal(err)
	}
	if err := validateIMLeaveStartup(imLeaveConfig{}, "invalid", "invalid", "invalid"); err != nil {
		t.Fatal("disabled listener changed existing startup:", err)
	}
}

func TestIMLeaveRuntimeOnlyRegistersUserTLSService(t *testing.T) {
	im, mock := testIMServer(t)
	imFiles, userFiles, otherFiles := leaveTestCertificates(t)
	runtime, err := newIMLeaveRuntime(imLeaveConfig{ListenOn: "127.0.0.1:0", Files: imFiles}, im.db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Stop)
	if services := runtime.server.GetServiceInfo(); len(services) != 1 || services["im.IMLeave"].Metadata != "leave.proto" {
		t.Fatalf("private listener registered unexpected services: %v", services)
	}
	go func() { _ = runtime.server.Serve(runtime.listener) }()
	connect := func(files rpcauth.CertificateFiles) *grpc.ClientConn {
		t.Helper()
		creds, err := rpcauth.NewServiceClientCredentials(files, "im.go-im.internal")
		if err != nil {
			t.Fatal(err)
		}
		conn, err := grpc.NewClient(runtime.listener.Addr().String(), grpc.WithTransportCredentials(creds))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request := &pb.CloseTeamGroupMembershipsRequest{TeamId: 200, UserId: 42, Generation: 3}
	fenceTestClose(mock, 0, 3, 2)
	result, err := pb.NewIMLeaveClient(connect(userFiles)).CloseTeamGroupMemberships(ctx, request)
	if err != nil || result.GetClosedThroughGeneration() != 3 {
		t.Fatalf("User service close = %+v, %v", result, err)
	}
	wrongCtx, wrongCancel := context.WithTimeout(context.Background(), time.Second)
	defer wrongCancel()
	result, err = pb.NewIMLeaveClient(connect(otherFiles)).CloseTeamGroupMemberships(wrongCtx, request)
	if result != nil || err == nil {
		t.Fatalf("Agent identity accepted: %+v, %v", result, err)
	}
	ordinary, err := pb.NewIMClient(connect(userFiles)).CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{})
	if ordinary != nil || status.Code(err) != codes.Unimplemented {
		t.Fatalf("ordinary IM exposed on private listener: %+v, %v", ordinary, err)
	}
	runtime.Stop()
	runtime.Stop()
}

func TestIMLeaveRuntimeRejectsMissingDatabaseAndBadCertificate(t *testing.T) {
	imFiles, _, _ := leaveTestCertificates(t)
	if runtime, err := newIMLeaveRuntime(imLeaveConfig{}, nil); err != nil || runtime != nil {
		t.Fatalf("disabled runtime = %v, %v", runtime, err)
	}
	if runtime, err := newIMLeaveRuntime(imLeaveConfig{ListenOn: "127.0.0.1:0", Files: imFiles}, nil); err == nil || runtime != nil {
		t.Fatalf("nil database accepted: %v, %v", runtime, err)
	}
	im, _ := testIMServer(t)
	bad := imFiles
	bad.CertFile = "missing.pem"
	if runtime, err := newIMLeaveRuntime(imLeaveConfig{ListenOn: "127.0.0.1:0", Files: bad}, im.db); err == nil || runtime != nil {
		t.Fatalf("missing certificate accepted: %v, %v", runtime, err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if runtime, err := newIMLeaveRuntime(imLeaveConfig{ListenOn: listener.Addr().String(), Files: imFiles}, im.db); err == nil || runtime != nil {
		t.Fatalf("busy port accepted: %v, %v", runtime, err)
	}
}
