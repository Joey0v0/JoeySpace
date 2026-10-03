package main

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testConfirmTaskServer struct {
	taskpb.UnimplementedTaskServer
	t     *testing.T
	calls int
}

func (s *testConfirmTaskServer) CreateTask(ctx context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
	s.calls++
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" {
		s.t.Errorf("Task token = %v", values)
	}
	if values := md.Get("idempotency-key"); len(values) != 1 || values[0] != "agent-task-9001-0" {
		s.t.Errorf("Task key = %v", values)
	}
	if req.GetTeamId() != 200 || req.GetTitle() != "修复缓存" || req.GetDescription() != "复核" ||
		req.GetAssigneeId() != 500 || req.GetDueAtUnixMs() != 1000 ||
		req.GetSourceGroupId() != 300 || req.GetSourceMessageId() != 600 {
		s.t.Errorf("Task fields = %v", req)
	}
	if s.calls == 1 {
		return nil, status.Error(codes.DeadlineExceeded, "simulated committed response loss")
	}
	return &taskpb.CreateTaskResponse{TaskId: 9223372036854775806}, nil
}

func configuredConfirmationRows(state, key string, taskID int64) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"team_id", "group_id", "initiator_id", "status", "title", "description",
		"assignee_id", "due_at_unix_ms", "source_message_id", "task_request_key", "task_id", "revision"}).
		AddRow(200, 300, 400, state, "修复缓存", "复核", 500, 1000, 600, key, taskID, 1)
}

func TestAgentStartupConnectsConfirmationAndRestartRetryToTask(t *testing.T) {
	t.Setenv("ARK_API_KEY", "test-key")
	t.Setenv("ARK_MODEL_ID", "ep-test-only")
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	userAddr := startDraftRPCDependency(t, func(server *grpc.Server) {
		userpb.RegisterUserServer(server, &testDraftUserServer{t: t})
	})
	imAddr := startDraftRPCDependency(t, func(server *grpc.Server) {
		impb.RegisterIMServer(server, &testDraftIMServer{t: t})
	})
	taskServer := &testConfirmTaskServer{t: t}
	taskAddr := startDraftRPCDependency(t, func(server *grpc.Server) { taskpb.RegisterTaskServer(server, taskServer) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer user-token"))
	req := &agentpb.ConfirmTaskDraftRequest{ExpectedRevision: 1, RunId: 9001, ExpectedTitle: "修复缓存", ExpectedDescription: "复核"}
	// Reconstruct the production server for the retry, while retaining the DB
	// record. All service calls use real local TCP; business DB/Task are fakes.
	for attempt := 0; attempt < 2; attempt++ {
		state, key := "waiting_confirmation", ""
		if attempt == 1 {
			state, key = "creating", "agent-task-9001-0"
		}
		mock.ExpectQuery(regexp.QuoteMeta("SELECT r.team_id")).WithArgs(int64(9001), int64(400)).
			WillReturnRows(configuredConfirmationRows(state, key, 0))
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT r\.team_id[\s\S]*FOR UPDATE`).WithArgs(int64(9001), int64(400)).
			WillReturnRows(configuredConfirmationRows(state, key, 0))
		if attempt == 0 {
			mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_request_key = ?")).
				WithArgs("agent-task-9001-0", int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).
				WithArgs("creating", int64(9001), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectCommit()
		if attempt == 1 {
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT r\.team_id[\s\S]*FOR UPDATE`).WithArgs(int64(9001), int64(400)).
				WillReturnRows(configuredConfirmationRows("creating", "agent-task-9001-0", 0))
			mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_id = ?")).
				WithArgs(int64(9223372036854775806), int64(9001)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).
				WithArgs("succeeded", int64(9001), int64(400)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
		}
		impl, closeClients, err := newAgentServer(ctx, imAddr, taskAddr, userAddr, db)
		if err != nil {
			t.Fatal(err)
		}
		agentAddr := startDraftRPCDependency(t, func(server *grpc.Server) { agentpb.RegisterAgentServer(server, impl) })
		conn, err := grpc.NewClient(agentAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			closeClients()
			t.Fatal(err)
		}
		resp, callErr := agentpb.NewAgentClient(conn).ConfirmTaskDraft(ctx, req)
		conn.Close()
		closeClients()
		if attempt == 0 {
			if resp != nil || status.Code(callErr) != codes.DeadlineExceeded {
				t.Fatalf("initial uncertain result = %v, %v", resp, callErr)
			}
		} else if callErr != nil || resp.GetTaskId() != 9223372036854775806 || resp.GetStatus() != "succeeded" {
			t.Fatalf("resumed confirmation = %v, %v", resp, callErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
	if taskServer.calls != 2 {
		t.Fatalf("Task calls = %d", taskServer.calls)
	}
}
