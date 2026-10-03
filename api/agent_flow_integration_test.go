package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	agent "github.com/yjydist/go-im/rpc/agent"
	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	flowTeamID  int64 = 9007199254740993
	flowGroupID int64 = 9007199254740995
	flowTaskID  int64 = 9007199254740997
)

// This model runs inside the Agent process and forces one real Eino tool call.
type flowModel struct{ tools []*schema.ToolInfo }

func (*flowModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream is not used")
}

func (*flowModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &flowModel{tools: tools}, nil
}

func (m *flowModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if len(m.tools) != 1 || m.tools[0].Name != "list_team_tasks" {
		return nil, errors.New("task tool was not bound")
	}
	if len(input) == 2 {
		if !strings.Contains(input[1].Content, "复核缓存") {
			return nil, errors.New("group history was not passed to model")
		}
		return schema.AssistantMessage("", []schema.ToolCall{{ID: "flow-call", Type: "function", Function: schema.FunctionCall{Name: "list_team_tasks", Arguments: `{}`}}}), nil
	}
	if len(input) == 4 && input[3].Role == schema.Tool && input[3].ToolCallID == "flow-call" && strings.Contains(input[3].Content, `"9007199254740997"`) {
		return schema.AssistantMessage("任务 9007199254740997 需要复核缓存。", nil), nil
	}
	return nil, errors.New("task result was not passed back to model")
}

// A separate test process hosts the Agent RPC; IM and Task remain TCP gRPC stubs.
func TestAgentFlowHelperProcess(t *testing.T) {
	if os.Getenv("GO_IM_AGENT_FLOW_HELPER") != "1" {
		return
	}
	imConn, err := grpc.NewClient(os.Getenv("GO_IM_FLOW_IM_ADDR"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer imConn.Close()
	taskConn, err := grpc.NewClient(os.Getenv("GO_IM_FLOW_TASK_ADDR"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer taskConn.Close()
	generator, err := agent.NewEinoGroupReplyGenerator(context.Background(), &flowModel{})
	if err != nil {
		t.Fatal(err)
	}
	reader := agent.NewContextReader(impb.NewIMClient(imConn), taskpb.NewTaskClient(taskConn))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := grpc.NewServer()
	agentpb.RegisterAgentServer(server, agent.NewServer(agent.NewGroupAnswerer(reader, generator)))
	fmt.Fprintln(os.Stdout, listener.Addr().String())
	if err := server.Serve(listener); err != nil {
		t.Fatal(err)
	}
}

type flowIMServer struct {
	impb.UnimplementedIMServer
	deny  atomic.Bool
	calls atomic.Int32
}

func (s *flowIMServer) ListTeamGroupMessages(ctx context.Context, req *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
	s.calls.Add(1)
	md, _ := metadata.FromIncomingContext(ctx)
	if req.GetTeamId() != flowTeamID || req.GetGroupId() != flowGroupID || req.GetLimit() != 20 || !validFlowToken(md) {
		return nil, status.Error(codes.InvalidArgument, "wrong IM scope or identity")
	}
	if s.deny.Load() {
		return nil, status.Error(codes.PermissionDenied, "private membership detail")
	}
	return &impb.ListTeamGroupMessagesResponse{Messages: []*impb.TeamGroupMessage{{Id: 42, ContentType: 1, Content: "复核缓存"}}}, nil
}

type flowTaskServer struct {
	taskpb.UnimplementedTaskServer
	fail  atomic.Bool
	calls atomic.Int32
}

func (s *flowTaskServer) ListTeamTasks(ctx context.Context, req *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
	s.calls.Add(1)
	md, _ := metadata.FromIncomingContext(ctx)
	if req.GetTeamId() != flowTeamID || req.GetLimit() != 20 || !validFlowToken(md) {
		return nil, status.Error(codes.InvalidArgument, "wrong Task scope or identity")
	}
	if s.fail.Load() {
		return nil, status.Error(codes.Unavailable, "private database detail")
	}
	return &taskpb.ListTeamTasksResponse{Tasks: []*taskpb.TaskItem{{TaskId: flowTaskID, Title: "复核缓存"}}}, nil
}

func validFlowToken(md metadata.MD) bool {
	values := md.Get("authorization")
	return len(values) == 1 && values[0] == "Bearer user-token"
}

func startFlowRPC(t *testing.T, register func(*grpc.Server)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	register(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	return listener.Addr().String()
}

func startFlowAgent(t *testing.T, imAddr, taskAddr string) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestAgentFlowHelperProcess$")
	cmd.Env = append(os.Environ(), "GO_IM_AGENT_FLOW_HELPER=1", "GO_IM_FLOW_IM_ADDR="+imAddr, "GO_IM_FLOW_TASK_ADDR="+taskAddr)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = cmd.Wait() })
	ready := make(chan string, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		if readErr != nil {
			ready <- ""
			return
		}
		ready <- strings.TrimSpace(line)
	}()
	select {
	case address := <-ready:
		if address == "" {
			t.Fatalf("Agent process did not start: %s", stderr.String())
		}
		return address
	case <-time.After(5 * time.Second):
		t.Fatalf("Agent process startup timed out: %s", stderr.String())
	}
	return ""
}

func TestAskAgentCrossProcessFlow(t *testing.T) {
	im := &flowIMServer{}
	tasks := &flowTaskServer{}
	imAddr := startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, im) })
	taskAddr := startFlowRPC(t, func(s *grpc.Server) { taskpb.RegisterTaskServer(s, tasks) })
	agentAddr := startFlowAgent(t, imAddr, taskAddr)
	conn, err := grpc.NewClient(agentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	handler := askAgentHandler(agentpb.NewAgentClient(conn))
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method != http.MethodPost || len(parts) != 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "teams" || parts[4] != "groups" || parts[6] != "ask" {
			http.NotFound(w, r)
			return
		}
		handler(w, pathvar.WithVars(r, map[string]string{"team_id": parts[3], "group_id": parts[5]}))
	}))
	defer httpServer.Close()

	request := func() (int, askAgentResponse) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/api/v1/teams/%d/groups/%d/ask", httpServer.URL, flowTeamID, flowGroupID), strings.NewReader(`{"question":"有哪些任务？"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer user-token")
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		var result askAgentResponse
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatalf("invalid HTTP response: %s: %v", body, err)
		}
		if strings.Contains(string(body), "private") {
			t.Fatalf("dependency details leaked: %s", body)
		}
		return resp.StatusCode, result
	}

	statusCode, result := request()
	if statusCode != 200 || result.Data == nil || result.Data.Answer != "任务 9007199254740997 需要复核缓存。" || im.calls.Load() != 1 || tasks.calls.Load() != 1 {
		t.Fatalf("success = %d %+v, IM/Task calls = %d/%d", statusCode, result, im.calls.Load(), tasks.calls.Load())
	}

	im.deny.Store(true)
	statusCode, result = request()
	if statusCode != 403 || result.Data != nil || im.calls.Load() != 2 || tasks.calls.Load() != 1 {
		t.Fatalf("IM denial = %d %+v, IM/Task calls = %d/%d", statusCode, result, im.calls.Load(), tasks.calls.Load())
	}

	im.deny.Store(false)
	tasks.fail.Store(true)
	statusCode, result = request()
	if statusCode != 503 || result.Data != nil || im.calls.Load() != 3 || tasks.calls.Load() != 2 {
		t.Fatalf("Task failure = %d %+v, IM/Task calls = %d/%d", statusCode, result, im.calls.Load(), tasks.calls.Load())
	}
}
