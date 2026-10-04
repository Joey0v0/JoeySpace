package main

import (
	"context"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	agent "github.com/yjydist/go-im/rpc/agent"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type triggerReadFlow struct {
	t          *testing.T
	mock       sqlmock.Sqlmock
	client     *agent.TriggerContextClient
	runtime    *imTriggerRuntime
	userMode   atomic.Int32 // 0 active, 1 left, 2 unavailable
	userCalls  atomic.Int32
	outbox     model.AgentTriggerOutbox
	source     model.Message
	otherFiles rpcauth.CertificateFiles
}

func newTriggerReadFlow(t *testing.T) *triggerReadFlow {
	t.Helper()
	im, mock := testIMServer(t)
	f := &triggerReadFlow{t: t, mock: mock}
	f.source = model.Message{ID: 9007199254740993, MsgID: "source-large-id", FromID: 9007199254740995,
		ToID: 300, ChatType: 2, SenderType: 1, ContentType: 1, Content: "@AI 整理任务 整理上线清单",
		CreatedAt: time.Date(2026, 10, 4, 2, 0, 0, 0, time.UTC)}
	f.outbox = model.AgentTriggerOutbox{MessageID: f.source.ID, Action: model.AgentTriggerAction, EventVersion: model.AgentTriggerVersion,
		MsgID: f.source.MsgID, ActorID: f.source.FromID, TeamID: 200, GroupID: f.source.ToID, Instruction: "整理上线清单",
		ReferenceTimeMS: f.source.CreatedAt.UnixMilli(), Published: false}
	userFiles := triggerClientCertificates(t)
	userAddr := startTriggerClientTestServer(t, userFiles["user.go-im.internal"], true, func(ctx context.Context, req *userpb.CheckTriggerTeamMemberRequest) (*userpb.CheckTriggerTeamMemberResponse, error) {
		if err := rpcauth.RequireServiceIdentity(ctx, "im.go-im.internal"); err != nil {
			return nil, err
		}
		md, _ := metadata.FromIncomingContext(ctx)
		if len(md.Get("authorization")) != 0 || len(md.Get("actor-id")) != 0 || req.ActorId != f.outbox.ActorID || req.TeamId != f.outbox.TeamID {
			t.Errorf("User received caller credentials or wrong persisted scope: %v %v", md, req)
			return nil, status.Error(codes.Internal, "bad persisted scope")
		}
		f.userCalls.Add(1)
		switch f.userMode.Load() {
		case 1:
			return nil, status.Error(codes.PermissionDenied, "private membership detail")
		case 2:
			return nil, status.Error(codes.Internal, "private database detail")
		}
		return &userpb.CheckTriggerTeamMemberResponse{ActorId: req.ActorId, TeamId: req.TeamId}, nil
	})
	teams, err := newTriggerTeamClient(triggerTeamClientConfig{Addr: userAddr, ServerDNSName: "user.go-im.internal", Files: userFiles["im.go-im.internal"]})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = teams.Close() })
	imFiles, agentFiles, otherFiles := botTestCertificates(t)
	f.otherFiles = otherFiles
	f.runtime, err = newIMTriggerRuntime(imTriggerConfig{ListenOn: "127.0.0.1:0", AgentDNSName: "agent.go-im.internal", Files: imFiles}, im.db, teams)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = f.runtime.server.Serve(f.runtime.listener) }()
	t.Cleanup(f.runtime.Stop)
	f.client = f.newClient(agentFiles)
	return f
}

func (f *triggerReadFlow) newClient(files rpcauth.CertificateFiles) *agent.TriggerContextClient {
	f.t.Helper()
	values := map[string]string{"AGENT_IM_TRIGGER_ADDR": f.runtime.listener.Addr().String(), "AGENT_IM_TRIGGER_SERVER_NAME": "im.go-im.internal",
		"AGENT_IM_TRIGGER_TLS_CERT_FILE": files.CertFile, "AGENT_IM_TRIGGER_TLS_KEY_FILE": files.KeyFile, "AGENT_IM_TRIGGER_TLS_CA_FILE": files.CAFile}
	client, err := agent.NewTriggerContextClient(func(name string) string { return values[name] })
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = client.Close() })
	return client
}

func flowTriggerMessageRows(messages ...model.Message) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "to_id", "chat_type", "content_type", "content", "created_at"})
	for _, m := range messages {
		rows.AddRow(m.ID, m.MsgID, m.FromID, m.SenderType, m.InitiatorID, m.ToID, m.ChatType, m.ContentType, m.Content, m.CreatedAt)
	}
	return rows
}

func (f *triggerReadFlow) expectSource(member bool) {
	r := f.outbox
	f.mock.ExpectQuery(regexp.QuoteMeta(triggerOutboxQuery)).WithArgs(r.MessageID).WillReturnRows(sqlmock.NewRows([]string{
		"message_id", "action", "event_version", "msg_id", "actor_id", "team_id", "group_id", "instruction", "reference_time_ms", "published",
	}).AddRow(r.MessageID, r.Action, r.EventVersion, r.MsgID, r.ActorID, r.TeamID, r.GroupID, r.Instruction, r.ReferenceTimeMS, r.Published))
	f.mock.ExpectQuery(regexp.QuoteMeta(triggerSourceQuery)).WithArgs(r.MessageID).WillReturnRows(flowTriggerMessageRows(f.source))
	rows := sqlmock.NewRows([]string{"id", "team_id", "user_id"})
	if member {
		rows.AddRow(r.GroupID, r.TeamID, r.ActorID)
	}
	f.mock.ExpectQuery(regexp.QuoteMeta(triggerMembershipQuery)).WithArgs(r.GroupID, r.TeamID, r.ActorID).WillReturnRows(rows)
}

func (f *triggerReadFlow) expectHistory(accessible bool) {
	rows := flowTriggerMessageRows()
	if accessible {
		older := f.source
		older.ID--
		older.MsgID, older.Content = "older-discussion", "检查发布文档"
		older.CreatedAt = older.CreatedAt.Add(-time.Minute)
		rows = flowTriggerMessageRows(f.source, older)
	}
	r := f.outbox
	f.mock.ExpectQuery(regexp.QuoteMeta(triggerHistoryQuery)).WithArgs(r.TeamID, r.ActorID, r.GroupID, r.MessageID).WillReturnRows(rows)
}

func TestTriggerReadFlowActualTLSUsesPersistedScopeAndRechecksCurrentTeam(t *testing.T) {
	f := newTriggerReadFlow(t)
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer old-user-token", "actor-id", "1", "team-id", "999"))
	for _, step := range []struct {
		name string
		mode int32
		want codes.Code
	}{{"active", 0, codes.OK}, {"left team", 1, codes.PermissionDenied}, {"User unavailable", 2, codes.Unavailable}, {"restored", 0, codes.OK}} {
		t.Run(step.name, func(t *testing.T) {
			f.userMode.Store(step.mode)
			f.expectSource(true)
			if step.want == codes.OK {
				f.expectHistory(true)
			}
			result, err := f.client.Read(ctx, f.source.ID)
			if status.Code(err) != step.want || err != nil && strings.Contains(err.Error(), "private") {
				t.Fatalf("result=%v err=%v", result, err)
			}
			if step.want == codes.OK {
				if result.ActorId != f.outbox.ActorID || result.TeamId != 200 || result.GroupId != 300 || result.MessageId != f.source.ID || result.ReferenceTimeUnixMs != f.outbox.ReferenceTimeMS || result.RequestKey != f.outbox.RequestKey() || len(result.Messages) != 2 || result.Messages[0].Content != f.source.Content || result.Messages[1].Id >= result.MessageId {
					t.Fatalf("persisted facts lost: %v", result)
				}
			} else if result != nil {
				t.Fatalf("denial leaked context: %v", result)
			}
		})
	}
	if f.userCalls.Load() != 4 {
		t.Fatalf("qualification cached or retried: %d", f.userCalls.Load())
	}
}

func TestTriggerReadFlowCurrentGroupRemovalBeforeAndAfterUserCheck(t *testing.T) {
	f := newTriggerReadFlow(t)
	f.expectSource(false)
	result, err := f.client.Read(context.Background(), f.source.ID)
	if result != nil || status.Code(err) != codes.PermissionDenied || f.userCalls.Load() != 0 {
		t.Fatalf("former group member reached User/history: %v %v calls=%d", result, err, f.userCalls.Load())
	}
	f.expectSource(true)
	f.expectHistory(false) // The last SQL join sees membership removed after User checked.
	result, err = f.client.Read(context.Background(), f.source.ID)
	if result != nil || status.Code(err) != codes.PermissionDenied || f.userCalls.Load() != 1 {
		t.Fatalf("late removal leaked history: %v %v calls=%d", result, err, f.userCalls.Load())
	}
}

func TestTriggerReadFlowRejectsWrongServiceAndAccidentalPlainRegistration(t *testing.T) {
	f := newTriggerReadFlow(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if result, err := f.newClient(f.otherFiles).Read(ctx, f.source.ID); result != nil || status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("same-CA wrong role: %v %v", result, err)
	}
	im, _ := testIMServer(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	plain := grpc.NewServer()
	pb.RegisterIMTriggerServer(plain, &triggerContextServer{db: im.db, agentDNSName: "agent.go-im.internal"})
	go func() { _ = plain.Serve(listener) }()
	t.Cleanup(func() { plain.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	callCtx, callCancel := context.WithTimeout(context.Background(), time.Second)
	defer callCancel()
	callCtx = metadata.NewOutgoingContext(callCtx, metadata.Pairs("authorization", "Bearer "+validIMToken(t), "service", "agent.go-im.internal"))
	if result, err := pb.NewIMTriggerClient(conn).ReadTaskTriggerContext(callCtx, &pb.ReadTaskTriggerContextRequest{MessageId: f.source.ID}); result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("token bypassed transport guard: %v %v", result, err)
	}
	if f.userCalls.Load() != 0 {
		t.Fatal("untrusted connection reached User")
	}
}
