package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	agent "github.com/yjydist/go-im/rpc/agent"
	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type confirmFlowUser struct{ userpb.UnimplementedUserServer }

func (*confirmFlowUser) GetMyInfo(ctx context.Context, _ *userpb.GetMyInfoRequest) (*userpb.GetUserInfoResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" {
		return nil, status.Error(codes.Unauthenticated, "wrong identity")
	}
	return &userpb.GetUserInfoResponse{Id: 400}, nil
}

type confirmFlowIM struct {
	impb.UnimplementedIMServer
	deny atomic.Bool
}

func (s *confirmFlowIM) CheckTeamGroupAccess(ctx context.Context, req *impb.CheckTeamGroupAccessRequest) (*impb.CheckTeamGroupAccessResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" || req.GetTeamId() != flowTeamID || req.GetGroupId() != flowGroupID {
		return nil, status.Error(codes.InvalidArgument, "wrong scope or identity")
	}
	if s.deny.Load() {
		return nil, status.Error(codes.PermissionDenied, "private membership detail")
	}
	return &impb.CheckTeamGroupAccessResponse{}, nil
}

type confirmFlowTask struct {
	taskpb.UnimplementedTaskServer
	calls atomic.Int32
}

func (s *confirmFlowTask) CreateTask(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" {
		return nil, status.Error(codes.Unauthenticated, "wrong identity")
	}
	if keys := md.Get("idempotency-key"); len(keys) != 1 || keys[0] != "agent-task-9001-0" ||
		req.GetTeamId() != flowTeamID || req.GetTitle() != "Task" || req.GetDescription() != "" ||
		req.GetSourceGroupId() != 0 || req.GetSourceMessageId() != 0 || req.GetAssigneeId() != 0 || req.GetDueAtUnixMs() != 0 {
		return nil, status.Error(codes.InvalidArgument, "changed frozen task request")
	}
	if s.calls.Add(1) == 1 {
		// Simulate a committed Task whose response is lost; retry resolves it.
		return nil, status.Error(codes.DeadlineExceeded, "private transport detail")
	}
	return &taskpb.CreateTaskResponse{TaskId: 9223372036854775806}, nil
}

func confirmFlowRows(state string, taskID int64) *sqlmock.Rows {
	key := "agent-task-9001-0"
	if state == "waiting_confirmation" {
		key = ""
	}
	return sqlmock.NewRows([]string{"team_id", "group_id", "initiator_id", "status", "title", "description",
		"assignee_id", "due_at_unix_ms", "source_message_id", "task_request_key", "task_id", "revision"}).
		AddRow(flowTeamID, flowGroupID, 400, state, "Task", "", 0, 0, 0, key, taskID, 1)
}

func TestConfirmDraftHTTPAndAgentRPCRecoverUncertainTaskResult(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	im := &confirmFlowIM{}
	tasks := &confirmFlowTask{}
	userAddr := startFlowRPC(t, func(s *grpc.Server) { userpb.RegisterUserServer(s, &confirmFlowUser{}) })
	imAddr := startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, im) })
	taskAddr := startFlowRPC(t, func(s *grpc.Server) { taskpb.RegisterTaskServer(s, tasks) })
	dial := func(addr string) *grpc.ClientConn {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	impl := agent.NewServer(nil) // Confirmation never needs a model.
	impl.ConfigureDraftAccess(db, userpb.NewUserClient(dial(userAddr)), impb.NewIMClient(dial(imAddr)))
	impl.ConfigureDraftConfirmation(db, taskpb.NewTaskClient(dial(taskAddr)))
	agentAddr := startFlowRPC(t, func(s *grpc.Server) { agentpb.RegisterAgentServer(s, impl) })
	agentClient := agentpb.NewAgentClient(dial(agentAddr))
	mux := http.NewServeMux()
	wrap := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			handler(w, pathvar.WithVars(r, map[string]string{"run_id": r.PathValue("run_id")}))
		}
	}
	mux.HandleFunc("POST /api/v1/agent/runs/{run_id}/confirm", wrap(confirmTaskDraftHandler(agentClient)))
	mux.HandleFunc("GET /api/v1/agent/runs/{run_id}/draft", wrap(getTaskDraftHandler(agentClient)))
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, suffix string, wantStatus int) agentDraftResponse {
		req, err := http.NewRequest(method, httpServer.URL+"/api/v1/agent/runs/9001/"+suffix, strings.NewReader(validDraftConfirmBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer user-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != wantStatus || strings.Contains(string(body), "private ") {
			t.Fatalf("HTTP = %d %s", resp.StatusCode, body)
		}
		var result agentDraftResponse
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	expectRead := func(state string, taskID int64) {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT r.team_id")).WithArgs(int64(9001), int64(400)).
			WillReturnRows(confirmFlowRows(state, taskID))
	}
	expectLock := func(state string, taskID int64) {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT r\.team_id[\s\S]*FOR UPDATE`).WithArgs(int64(9001), int64(400)).
			WillReturnRows(confirmFlowRows(state, taskID))
	}
	// Initial confirmation commits the frozen intent, but Task response is lost.
	expectRead("waiting_confirmation", 0)
	expectLock("waiting_confirmation", 0)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_request_key = ?")).
		WithArgs("agent-task-9001-0", int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).
		WithArgs("creating", int64(9001), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if result := request(http.MethodPost, "confirm", 504); result.Code == 0 || result.Data != nil {
		t.Fatalf("uncertain response = %+v", result)
	}
	expectRead("creating", 0)
	if result := request(http.MethodGet, "draft", 200); result.Data.Status != "creating" || result.Data.TaskID != 0 {
		t.Fatalf("pending read = %+v", result)
	}
	// Explicit retry uses the same payload/key and persists the existing Task ID.
	expectRead("creating", 0)
	expectLock("creating", 0)
	mock.ExpectCommit()
	expectLock("creating", 0)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_id = ?")).
		WithArgs(int64(9223372036854775806), int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).
		WithArgs("succeeded", int64(9001), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if result := request(http.MethodPost, "confirm", 200); result.Data.Status != "succeeded" || result.Data.TaskID != 9223372036854775806 {
		t.Fatalf("retry result = %+v", result)
	}
	// Repeated confirmation returns the saved result without another Task call.
	expectRead("succeeded", 9223372036854775806)
	expectLock("succeeded", 9223372036854775806)
	mock.ExpectCommit()
	if result := request(http.MethodPost, "confirm", 200); result.Data.TaskID != 9223372036854775806 {
		t.Fatalf("repeated result = %+v", result)
	}
	// Even an already-succeeded confirmation must enforce current group access.
	im.deny.Store(true)
	expectRead("succeeded", 9223372036854775806)
	if result := request(http.MethodPost, "confirm", 403); result.Data != nil || result.Code == 0 {
		t.Fatalf("revoked access = %+v", result)
	}
	if tasks.calls.Load() != 2 {
		t.Fatalf("Task calls = %d", tasks.calls.Load())
	}
}
