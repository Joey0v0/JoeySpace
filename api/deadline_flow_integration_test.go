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

// Include milliseconds so restoring the frozen intent must not round it.
const deadlineFlowDue int64 = 1791097200123

type deadlineFlowTask struct {
	taskpb.UnimplementedTaskServer
	calls atomic.Int32
}

func (s *deadlineFlowTask) CreateTask(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if auth := md.Get("authorization"); len(auth) != 1 || auth[0] != "Bearer user-token" {
		return nil, status.Error(codes.Unauthenticated, "wrong identity")
	}
	if key := md.Get("idempotency-key"); len(key) != 1 || key[0] != "agent-task-9001-0" ||
		req.GetDueAtUnixMs() != deadlineFlowDue || req.GetTeamId() != flowTeamID || req.GetTitle() != "Task" ||
		req.GetDescription() != "" || req.GetAssigneeId() != 0 || req.GetSourceGroupId() != 0 || req.GetSourceMessageId() != 0 {
		return nil, status.Error(codes.InvalidArgument, "changed frozen deadline")
	}
	if s.calls.Add(1) == 1 {
		return nil, status.Error(codes.DeadlineExceeded, "private lost task response")
	}
	return &taskpb.CreateTaskResponse{TaskId: 88}, nil
}

func deadlineFlowRows(state string, due, revision, taskID int64) *sqlmock.Rows {
	key := "agent-task-9001-0"
	if state == "waiting_confirmation" {
		key = ""
	}
	return sqlmock.NewRows([]string{"team_id", "group_id", "initiator_id", "status", "title", "description", "assignee_id",
		"due_at_unix_ms", "source_message_id", "task_request_key", "task_id", "assignee_name", "assignee_resolution", "revision"}).
		AddRow(flowTeamID, flowGroupID, 400, state, "Task", "", 0, due, 0, key, taskID, "", "none", revision)
}

// Production handlers and Agent store adapters use actual HTTP/TCP gRPC.
// SQL and User/IM/Task are substitutes; no real database/model is involved.
func TestDeadlineHTTPAndAgentRPCReviewEditAndFrozenRetry(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	users, im, tasks := &confirmFlowUser{}, &confirmFlowIM{}, &deadlineFlowTask{}
	dial := func(addr string) *grpc.ClientConn {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	userAddr := startFlowRPC(t, func(s *grpc.Server) { userpb.RegisterUserServer(s, users) })
	imAddr := startFlowRPC(t, func(s *grpc.Server) { impb.RegisterIMServer(s, im) })
	taskAddr := startFlowRPC(t, func(s *grpc.Server) { taskpb.RegisterTaskServer(s, tasks) })
	impl := agent.NewServer(nil)
	impl.ConfigureDraftAccess(db, userpb.NewUserClient(dial(userAddr)), impb.NewIMClient(dial(imAddr)))
	impl.ConfigureDraftConfirmation(db, taskpb.NewTaskClient(dial(taskAddr)))
	addr := startFlowRPC(t, func(s *grpc.Server) { agentpb.RegisterAgentServer(s, impl) })
	client := agentpb.NewAgentClient(dial(addr))
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			h(w, pathvar.WithVars(r, map[string]string{"run_id": r.PathValue("run_id")}))
		}
	}
	mux.HandleFunc("GET /runs/{run_id}/draft", wrap(getTaskDraftHandler(client)))
	mux.HandleFunc("PUT /runs/{run_id}/deadline", wrap(editTaskDraftDeadlineHandler(client)))
	mux.HandleFunc("POST /runs/{run_id}/confirm", wrap(confirmTaskDraftHandler(client)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	httpClient := &http.Client{Timeout: 5 * time.Second}
	request := func(method, suffix, body string, want int) agentDraftResponse {
		req, err := http.NewRequest(method, server.URL+"/runs/9001/"+suffix, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer user-token")
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		content, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want || strings.Contains(string(content), "private ") {
			t.Fatalf("%s %s: %d %s", method, suffix, resp.StatusCode, content)
		}
		var result agentDraftResponse
		if err := json.Unmarshal(content, &result); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	expectRead := func(state string, due, revision, taskID int64) {
		mock.ExpectQuery(regexp.QuoteMeta("SELECT r.team_id")).WithArgs(int64(9001), int64(400)).WillReturnRows(deadlineFlowRows(state, due, revision, taskID))
	}
	expectLock := func(state string, due, revision, taskID int64) {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT r\.team_id[\s\S]*FOR UPDATE`).WithArgs(int64(9001), int64(400)).WillReturnRows(deadlineFlowRows(state, due, revision, taskID))
	}
	expectRead("waiting_confirmation", 0, 1, 0)
	initial := request(http.MethodGet, "draft", "", 200)
	if initial.Data.Draft.DueAtUnixMs != 0 || initial.Data.Draft.Revision != 1 {
		t.Fatalf("initial deadline: %+v", initial)
	}
	expectRead("waiting_confirmation", 0, 1, 0)
	expectLock("waiting_confirmation", 0, 1, 0)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET due_at_unix_ms = ?, revision = revision + 1")).
		WithArgs(deadlineFlowDue, int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	const editBody = `{"due_at_unix_ms":1791097200123,"expected_revision":"1"}`
	edited := request(http.MethodPut, "deadline", editBody, 200)
	if edited.Data.Draft.DueAtUnixMs != deadlineFlowDue || edited.Data.Draft.Revision != 2 || edited.Data.Draft.Title != "Task" {
		t.Fatalf("edited deadline: %+v", edited)
	}
	expectRead("waiting_confirmation", deadlineFlowDue, 2, 0)
	request(http.MethodPut, "deadline", editBody, 409)
	// A new version alone cannot let an old client confirm an unseen deadline.
	expectRead("waiting_confirmation", deadlineFlowDue, 2, 0)
	request(http.MethodPost, "confirm", `{"expected_title":"Task","expected_description":"","expected_revision":"2"}`, 409)
	expectRead("waiting_confirmation", deadlineFlowDue, 2, 0)
	request(http.MethodPost, "confirm", `{"expected_title":"Task","expected_description":"","expected_revision":"2","expected_due_at_unix_ms":0}`, 409)
	if tasks.calls.Load() != 0 {
		t.Fatal("unreviewed deadline created a task")
	}
	const confirmBody = `{"expected_title":"Task","expected_description":"","expected_revision":"2","expected_assignee_id":"0","expected_due_at_unix_ms":1791097200123}`
	expectRead("waiting_confirmation", deadlineFlowDue, 2, 0)
	expectLock("waiting_confirmation", deadlineFlowDue, 2, 0)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_request_key = ?")).WithArgs("agent-task-9001-0", int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).WithArgs("creating", int64(9001), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	request(http.MethodPost, "confirm", confirmBody, 504)
	expectRead("creating", deadlineFlowDue, 2, 0)
	pending := request(http.MethodGet, "draft", "", 200)
	if pending.Data.Draft.DueAtUnixMs != deadlineFlowDue || pending.Data.Draft.Revision != 2 || pending.Data.Status != "creating" {
		t.Fatalf("frozen deadline: %+v", pending)
	}
	expectRead("creating", deadlineFlowDue, 2, 0)
	request(http.MethodPut, "deadline", `{"due_at_unix_ms":0,"expected_revision":"2"}`, 409)
	expectRead("creating", deadlineFlowDue, 2, 0)
	expectLock("creating", deadlineFlowDue, 2, 0)
	mock.ExpectCommit()
	expectLock("creating", deadlineFlowDue, 2, 0)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_id = ?")).WithArgs(int64(88), int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).WithArgs("succeeded", int64(9001), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	result := request(http.MethodPost, "confirm", confirmBody, 200)
	if result.Data.TaskID != 88 || result.Data.Draft.Revision != 2 || result.Data.Draft.DueAtUnixMs != deadlineFlowDue {
		t.Fatalf("recovered deadline: %+v", result)
	}
	expectRead("succeeded", deadlineFlowDue, 2, 88)
	expectLock("succeeded", deadlineFlowDue, 2, 88)
	mock.ExpectCommit()
	request(http.MethodPost, "confirm", confirmBody, 200)
	if tasks.calls.Load() != 2 {
		t.Fatalf("successful replay created again: %d", tasks.calls.Load())
	}
	im.deny.Store(true)
	expectRead("succeeded", deadlineFlowDue, 2, 88)
	request(http.MethodPost, "confirm", confirmBody, 403)
}
