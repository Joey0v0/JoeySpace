package agent

import (
	"context"
	"database/sql/driver"
	"net"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/bwmarrin/snowflake"
	"github.com/cloudwego/eino/schema"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type assigneeFlowUser struct {
	userpb.UnimplementedUserServer
	lookups atomic.Int32
}

func (s *assigneeFlowUser) GetMyInfo(ctx context.Context, _ *userpb.GetMyInfoRequest) (*userpb.GetUserInfoResponse, error) {
	if !assigneeFlowAuthorized(ctx) {
		return nil, status.Error(codes.Unauthenticated, "original token missing")
	}
	return &userpb.GetUserInfoResponse{Id: 400}, nil
}
func (s *assigneeFlowUser) ResolveTeamMember(ctx context.Context, req *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
	s.lookups.Add(1)
	if !assigneeFlowAuthorized(ctx) || req.GetTeamId() != 200 || req.GetName() != "张三" {
		return nil, status.Error(codes.PermissionDenied, "wrong scope")
	}
	return &userpb.ResolveTeamMemberResponse{Candidates: []*userpb.TeamMember{{UserId: 77, Username: "zhang-san", Nickname: "张三"}}}, nil
}
func assigneeFlowAuthorized(ctx context.Context) bool {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	return len(values) == 1 && values[0] == "Bearer user-token" && len(md.Get("idempotency-key")) == 0
}

type assigneeFlowIM struct{ impb.UnimplementedIMServer }

func (s *assigneeFlowIM) ListTeamGroupMessages(ctx context.Context, req *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
	if !assigneeFlowAuthorized(ctx) || req.GetTeamId() != 200 || req.GetGroupId() != 300 {
		return nil, status.Error(codes.PermissionDenied, "wrong group")
	}
	return &impb.ListTeamGroupMessagesResponse{Messages: []*impb.TeamGroupMessage{{Id: 600, ContentType: 1, Content: "张三整理文档"}}}, nil
}
func (s *assigneeFlowIM) CheckTeamGroupAccess(ctx context.Context, req *impb.CheckTeamGroupAccessRequest) (*impb.CheckTeamGroupAccessResponse, error) {
	if !assigneeFlowAuthorized(ctx) || req.GetTeamId() != 200 || req.GetGroupId() != 300 {
		return nil, status.Error(codes.PermissionDenied, "wrong group")
	}
	return &impb.CheckTeamGroupAccessResponse{}, nil
}

type assigneeFlowTasks struct {
	taskpb.UnimplementedTaskServer
	calls atomic.Int32
}

func (s *assigneeFlowTasks) CreateTask(context.Context, *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
	s.calls.Add(1)
	return nil, status.Error(codes.Internal, "unexpected task creation")
}

type captureAssigneeRunID struct{ value *atomic.Int64 }

func (a captureAssigneeRunID) Match(value driver.Value) bool {
	id, ok := value.(int64)
	if ok && id > 0 {
		a.value.Store(id)
		return true
	}
	return false
}

func TestAssigneeProductionPreparationPersistsReadsAndReplaysOverTCP(t *testing.T) {
	store, mock := testDraftStore(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	users := &assigneeFlowUser{}
	tasks := &assigneeFlowTasks{}
	userpb.RegisterUserServer(server, users)
	impb.RegisterIMServer(server, &assigneeFlowIM{})
	taskpb.RegisterTaskServer(server, tasks)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	var modelCalls atomic.Int32
	generator, err := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
		modelCalls.Add(1)
		return schema.AssistantMessage(`{"title":"整理文档","description":"","source_message_id":"600","assignee_name":"张三","deadline_text":"","deadline_source":"none","deadline_source_message_id":"0"}`, nil), nil
	}))
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	node, err := snowflake.NewNode(5)
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	impl := NewServer(nil)
	impl.ConfigureDraftPreparation(store.db, userpb.NewUserClient(conn), impb.NewIMClient(conn), generator, node)
	impl.ConfigureDraftAccess(store.db, userpb.NewUserClient(conn), impb.NewIMClient(conn))
	impl.ConfigureDraftConfirmation(store.db, taskpb.NewTaskClient(conn))
	pb.RegisterAgentServer(server, impl)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client := pb.NewAgentClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prepareCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer user-token", "idempotency-key", "request-1"))
	readCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer user-token"))
	lookup := regexp.QuoteMeta("SELECT id, team_id, group_id, request_fingerprint FROM agent_runs")
	mock.ExpectQuery(lookup).WithArgs(int64(400), "request-1").WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"}))
	mock.ExpectBegin()
	var runID atomic.Int64
	mock.ExpectExec("INSERT INTO agent_runs").WithArgs(captureAssigneeRunID{&runID}, int64(200), int64(300), int64(400), "request-1", draftPreparationFingerprint(200, 300, "提取待办"), "waiting_confirmation").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO agent_task_drafts").WithArgs(sqlmock.AnyArg(), "整理文档", "", int64(77), int64(0), int64(600), "张三", "matched", "", "none", int64(0), int64(0), "Asia/Shanghai", "none", "", int64(0), int64(0)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	request := &pb.PrepareTaskDraftRequest{TeamId: 200, GroupId: 300, Instruction: "提取待办"}
	prepared, err := client.PrepareTaskDraft(prepareCtx, request)
	if err != nil || prepared.GetRunId() != runID.Load() || runID.Load() <= 0 {
		t.Fatalf("prepare: %v, %v", prepared, err)
	}
	run := taskDraftRun{Revision: 1, ID: runID.Load(), Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, Status: draftWaitingConfirmation, Draft: taskDraft{Deadline: draftDeadlineMetadata{Source: "none", Timezone: draftDeadlineTimezone, Resolution: "none"}, Title: "整理文档", SourceMessageID: 600, AssigneeID: 77, AssigneeName: "张三", AssigneeResolution: assigneeMatched}}
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator)).WithArgs(run.ID, int64(400)).WillReturnRows(confirmationRows(run))
	response, err := client.GetTaskDraft(readCtx, &pb.GetTaskDraftRequest{RunId: run.ID})
	if err != nil || response.GetDraft().GetAssigneeId() != 77 || response.GetDraft().GetAssigneeName() != "张三" || response.GetDraft().GetAssigneeResolution() != "matched" {
		t.Fatalf("read saved result: %v, %v", response, err)
	}
	mock.ExpectQuery(lookup).WithArgs(int64(400), "request-1").WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"}).AddRow(run.ID, 200, 300, draftPreparationFingerprint(200, 300, "提取待办")))
	replayed, err := client.PrepareTaskDraft(prepareCtx, request)
	if err != nil || replayed.GetRunId() != run.ID || modelCalls.Load() != 1 || users.lookups.Load() != 1 {
		t.Fatalf("replay changed intent: %v, %v, model=%d lookup=%d", replayed, err, modelCalls.Load(), users.lookups.Load())
	}
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator)).WithArgs(run.ID, int64(400)).WillReturnRows(confirmationRows(run))
	confirmed, err := client.ConfirmTaskDraft(readCtx, &pb.ConfirmTaskDraftRequest{ExpectedRevision: 1, RunId: run.ID, ExpectedTitle: "整理文档"})
	if confirmed != nil || status.Code(err) != codes.FailedPrecondition || tasks.calls.Load() != 0 {
		t.Fatalf("old confirmation bypassed review: %v, %v", confirmed, err)
	}
}
