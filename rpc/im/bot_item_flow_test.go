package main

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	driver "github.com/go-sql-driver/mysql"
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

const botItemFlowRunID int64 = 9007199254740993

type botItemFlow struct {
	t           *testing.T
	im          *imServer
	mock        sqlmock.Sqlmock
	runtime     *botReplyRuntime
	client      pb.IMBotClient
	serverFiles rpcauth.CertificateFiles
	agentFiles  rpcauth.CertificateFiles
	otherFiles  rpcauth.CertificateFiles
	token       string
	teamRevoked atomic.Bool
}

func newBotItemFlow(t *testing.T) *botItemFlow {
	t.Helper()
	serverFiles, agentFiles, otherFiles := botTestCertificates(t)
	im, mock := testIMServer(t)
	f := &botItemFlow{t: t, im: im, mock: mock, serverFiles: serverFiles, agentFiles: agentFiles, otherFiles: otherFiles, token: validIMToken(t)}
	im.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if auth := md.Get("authorization"); req.GetTeamId() != 200 || len(auth) != 1 || auth[0] != "Bearer "+f.token {
			return status.Error(codes.InvalidArgument, "changed original user or team scope")
		}
		if f.teamRevoked.Load() {
			return status.Error(codes.PermissionDenied, "private revoked team membership")
		}
		return nil
	})
	runtime, err := newBotReplyRuntime(botReplyConfig{ListenOn: "127.0.0.1:0", BotCode: "task-assistant", AgentDNSName: "agent.go-im.internal", Files: serverFiles,
		Brokers: []string{"127.0.0.1:1"}, Topic: "chat_messages"}, im)
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = runtime
	go runtime.server.Serve(runtime.listener)
	t.Cleanup(runtime.Stop)
	if _, ordinary := runtime.server.GetServiceInfo()["im.IM"]; ordinary {
		t.Fatal("ordinary IM leaked into bot TLS listener")
	}
	if _, bot := runtime.server.GetServiceInfo()["im.IMBot"]; !bot {
		t.Fatal("production bot listener missing IMBot service")
	}
	f.client = f.newClient(agentFiles, "im.go-im.internal")
	return f
}

func (f *botItemFlow) newClient(files rpcauth.CertificateFiles, serverName string) pb.IMBotClient {
	return f.newClientAt(files, serverName, f.runtime.listener.Addr().String())
}

func (f *botItemFlow) newClientAt(files rpcauth.CertificateFiles, serverName, address string) pb.IMBotClient {
	f.t.Helper()
	values := map[string]string{"AGENT_IM_BOT_ADDR": address, "AGENT_IM_BOT_SERVER_NAME": serverName,
		"AGENT_IM_BOT_TLS_CERT_FILE": files.CertFile, "AGENT_IM_BOT_TLS_KEY_FILE": files.KeyFile, "AGENT_IM_BOT_TLS_CA_FILE": files.CAFile}
	client, closeClient, err := agent.NewBotReplyClient(func(name string) string { return values[name] })
	if err != nil || client == nil {
		f.t.Fatalf("production Agent TLS client: %v", err)
	}
	f.t.Cleanup(closeClient)
	return client
}

func (f *botItemFlow) ctx(token bool) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if token {
		ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+f.token))
	}
	return ctx, cancel
}

func botItemFlowRecord(t *testing.T, index int32) botSendRecord {
	t.Helper()
	id, err := model.BotTaskItemMsgID(botItemFlowRunID, index)
	if err != nil {
		t.Fatal(err)
	}
	i := validBotIntent(t)
	return botSendRecord{MsgID: id, RunID: botItemFlowRunID, ItemIndex: index, BotID: i.BotID, InitiatorID: i.InitiatorID, TeamID: i.TeamID, GroupID: i.GroupID,
		Content: i.Content, TimestampUnixMs: 1790000000123, Accepted: true}
}

func (f *botItemFlow) expectExisting(record botSendRecord, submittedContent string) {
	expectBotAccess(f.mock)
	expectBotProfile(f.mock, true)
	f.mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `im_bot_sends`")).WithArgs(record.MsgID, record.RunID, int64(record.ItemIndex), record.BotID, record.InitiatorID, record.TeamID, record.GroupID, submittedContent,
		sqlmock.AnyArg(), false, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnError(&driver.MySQLError{Number: 1062})
	f.mock.ExpectQuery(regexp.QuoteMeta("SELECT * FROM `im_bot_sends` WHERE msg_id = ? LIMIT ?")).WithArgs(record.MsgID, 1).WillReturnRows(botSendRows(record))
}

func (f *botItemFlow) item(client pb.IMBotClient, index *int32, content string, withToken bool, want codes.Code, wantID string) {
	f.t.Helper()
	ctx, cancel := f.ctx(withToken)
	defer cancel()
	result, err := client.PostTaskCreatedCardItem(ctx, &pb.PostTaskCreatedCardItemRequest{RunId: botItemFlowRunID, ItemIndex: index, TeamId: 200, GroupId: 300, Content: content})
	if status.Code(err) != want {
		f.t.Fatalf("item=%v response=%v err=%v want=%v", index, result, err, want)
	}
	if want == codes.OK {
		if result == nil || !result.GetAccepted() || result.GetMsgId() != wantID {
			f.t.Fatalf("wrong item acceptance: %v", result)
		}
	} else if result != nil {
		f.t.Fatalf("rejected request returned accepted data: %v", result)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		f.t.Fatal(err)
	}
}

// This is the unmodified production newBotReplyRuntime plus production Agent
// NewBotReplyClient over actual TCP mTLS. SQL returns only durable Accepted=true
// records, so the runtime's lazy real Kafka writer is never called. Publication
// and uncertain Kafka retries are covered separately with a writer substitute.
func TestBotItemProductionTLSRuntimeKeepsDistinctItemsAndLegacyZero(t *testing.T) {
	f := newBotItemFlow(t)
	for _, index := range []int32{1, 4} {
		record := botItemFlowRecord(t, index)
		f.expectExisting(record, record.Content)
		f.item(f.client, &index, record.Content, true, codes.OK, fmt.Sprintf("bot-task:9007199254740993:%d", index))
	}
	zero := int32(0)
	record := botItemFlowRecord(t, zero)
	f.expectExisting(record, record.Content)
	ctx, cancel := f.ctx(true)
	result, err := f.client.PostTaskCreatedCard(ctx, &pb.PostTaskCreatedCardRequest{RunId: botItemFlowRunID, TeamId: 200, GroupId: 300, Content: record.Content})
	cancel()
	if err != nil || !result.GetAccepted() || result.GetMsgId() != "bot-task:9007199254740993" {
		t.Fatalf("legacy zero: %v %v", result, err)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	f.expectExisting(record, record.Content)
	f.item(f.client, &zero, record.Content, true, codes.OK, "bot-task:9007199254740993")

	// A different valid version-1 card cannot reuse an already accepted item ID.
	changed, err := model.EncodeTaskCreatedCard(9007199254740993, "换过的卡片标题")
	if err != nil {
		t.Fatal(err)
	}
	one := int32(1)
	record = botItemFlowRecord(t, one)
	f.expectExisting(record, changed)
	f.item(f.client, &one, changed, true, codes.AlreadyExists, "")

	// Accepted replay must recheck group access before profile/storage/Kafka.
	f.mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
	f.item(f.client, &one, record.Content, true, codes.PermissionDenied, "")
	f.teamRevoked.Store(true)
	f.mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
	four := int32(4)
	f.item(f.client, &four, record.Content, true, codes.PermissionDenied, "")
}

func TestBotItemProductionTLSRuntimeRejectsMissingIdentityAndOtherListeners(t *testing.T) {
	f := newBotItemFlow(t)
	content := validBotIntent(t).Content
	one := int32(1)
	f.item(f.client, nil, content, true, codes.InvalidArgument, "")
	for _, index := range []int32{-1, 5} {
		f.item(f.client, &index, content, true, codes.InvalidArgument, "")
	}
	f.item(f.client, &one, content, false, codes.Unauthenticated, "")
	f.item(f.newClient(f.otherFiles, "im.go-im.internal"), &one, content, true, codes.Unavailable, "")
	f.item(f.newClient(f.agentFiles, "wrong.im.internal"), &one, content, true, codes.Unavailable, "")

	// A valid user Token does not replace TLS on the dedicated bot port.
	plain, err := grpc.NewClient(f.runtime.listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { plain.Close() })
	f.item(pb.NewIMBotClient(plain), &one, content, true, codes.Unavailable, "")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ordinary := grpc.NewServer()
	registerOrdinaryIM(ordinary, f.im)
	go ordinary.Serve(listener)
	t.Cleanup(func() { ordinary.Stop(); listener.Close() })
	if _, leaked := ordinary.GetServiceInfo()["im.IMBot"]; leaked {
		t.Fatal("bot API registered on ordinary IM listener")
	}
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	f.item(pb.NewIMBotClient(conn), &one, content, true, codes.Unimplemented, "")
}

type botItemLegacyServer struct {
	pb.UnimplementedIMBotServer
	oldCalls atomic.Int32
}

func (s *botItemLegacyServer) PostTaskCreatedCard(context.Context, *pb.PostTaskCreatedCardRequest) (*pb.PostTaskCreatedCardResponse, error) {
	s.oldCalls.Add(1)
	return &pb.PostTaskCreatedCardResponse{MsgId: "bot-task:9007199254740993", Accepted: true}, nil
}

func TestBotItemProductionAgentClientDoesNotFallbackToLegacyZero(t *testing.T) {
	f := newBotItemFlow(t)
	creds, err := rpcauth.NewIMServerCredentials(f.serverFiles, "agent.go-im.internal")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(creds), grpc.MaxRecvMsgSize(8192))
	legacy := &botItemLegacyServer{}
	// A compatibility double uses real mTLS but only the old business method.
	// Embedded defaults model an old peer that does not implement item posting.
	pb.RegisterIMBotServer(server, legacy)
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	client := f.newClientAt(f.agentFiles, "im.go-im.internal", listener.Addr().String())
	one := int32(1)
	f.item(client, &one, validBotIntent(t).Content, true, codes.Unimplemented, "")
	if legacy.oldCalls.Load() != 0 {
		t.Fatal("new item call silently fell back to the old zero-item method")
	}
}
