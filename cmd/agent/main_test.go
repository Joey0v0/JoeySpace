package main

import (
	"context"
	"net"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	agentpb "github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/zrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testDraftUserServer struct {
	userpb.UnimplementedUserServer
	t *testing.T
}

func (s *testDraftUserServer) GetMyInfo(ctx context.Context, _ *userpb.GetMyInfoRequest) (*userpb.GetUserInfoResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" {
		s.t.Errorf("User token = %v", values)
	}
	return &userpb.GetUserInfoResponse{Id: 400}, nil
}

type testDraftIMServer struct {
	impb.UnimplementedIMServer
	t *testing.T
}

func (s *testDraftUserServer) CheckTeamMemberByID(ctx context.Context, req *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if md.Get("authorization")[0] != "Bearer user-token" || req.GetTeamId() != 200 || req.GetUserId() != 500 {
		s.t.Errorf("assignee membership: %v, %v", req, md)
	}
	return &userpb.CheckTeamMemberByIDResponse{}, nil
}

func (s *testDraftIMServer) CheckTeamGroupAccess(ctx context.Context, req *impb.CheckTeamGroupAccessRequest) (*impb.CheckTeamGroupAccessResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if values := md.Get("authorization"); len(values) != 1 || values[0] != "Bearer user-token" || req.GetTeamId() != 200 || req.GetGroupId() != 300 {
		s.t.Errorf("IM scope = %v, token = %v", req, values)
	}
	return &impb.CheckTeamGroupAccessResponse{}, nil
}

func startDraftRPCDependency(t *testing.T, register func(*grpc.Server)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	register(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

func TestAgentConfigLoadsLocalAndContainerAddresses(t *testing.T) {
	for _, tc := range []struct{ file, address string }{
		{"../../rpc/agent/etc/agent.yaml", "127.0.0.1:9004"},
		{"../../deploy/agent-rpc.yaml", "0.0.0.0:9004"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			var c zrpc.RpcServerConf
			if err := conf.Load(tc.file, &c); err != nil {
				t.Fatal(err)
			}
			if c.ListenOn != tc.address || c.Timeout != 20000 {
				t.Fatalf("Agent config = address %q, timeout %d", c.ListenOn, c.Timeout)
			}
		})
	}
}

func TestAgentStartupRejectsMissingDependenciesAndModelCredentials(t *testing.T) {
	t.Setenv("ARK_API_KEY", "test-key")
	t.Setenv("ARK_MODEL_ID", "")
	for _, tc := range []struct {
		imAddr, taskAddr, userAddr string
		db                         *gorm.DB
		wantCode                   codes.Code
	}{
		{"", "127.0.0.1:9003", "127.0.0.1:9001", &gorm.DB{}, codes.Unknown},
		{"127.0.0.1:9002", "", "127.0.0.1:9001", &gorm.DB{}, codes.Unknown},
		{"127.0.0.1:9002", "127.0.0.1:9003", "", &gorm.DB{}, codes.Unknown},
		{"127.0.0.1:9002", "127.0.0.1:9003", "127.0.0.1:9001", nil, codes.Unknown},
		{"127.0.0.1:9002", "127.0.0.1:9003", "127.0.0.1:9001", &gorm.DB{}, codes.FailedPrecondition},
	} {
		impl, closeClients, err := newAgentServer(context.Background(), tc.imAddr, tc.taskAddr, tc.userAddr, tc.db)
		if impl != nil || closeClients != nil || err == nil || status.Code(err) != tc.wantCode {
			t.Fatalf("startup = %v, close configured = %t, %v", impl, closeClients != nil, err)
		}
		if strings.Contains(err.Error(), "test-key") {
			t.Fatalf("startup error leaked a credential: %v", err)
		}
	}
}

func TestAgentStartupWiresDependenciesWithoutCallingProvider(t *testing.T) {
	t.Setenv("ARK_API_KEY", "test-key")
	t.Setenv("ARK_MODEL_ID", "ep-test-only")
	impl, closeClients, err := newAgentServer(context.Background(), "127.0.0.1:9002", "127.0.0.1:9003", "127.0.0.1:9001", &gorm.DB{})
	if err != nil || impl == nil || closeClients == nil {
		t.Fatalf("startup = %v, close configured = %t, %v", impl, closeClients != nil, err)
	}
	closeClients()
}

func TestAgentStartupRejectsPartialBotConfigurationBeforeModelSetup(t *testing.T) {
	t.Setenv("AGENT_IM_BOT_ADDR", "im-rpc:9005")
	t.Setenv("AGENT_IM_BOT_SERVER_NAME", "")
	t.Setenv("ARK_API_KEY", "")
	impl, closeClients, err := newAgentServer(context.Background(), "127.0.0.1:9002", "127.0.0.1:9003", "127.0.0.1:9001", &gorm.DB{})
	if impl != nil || closeClients != nil || err == nil || !strings.Contains(err.Error(), "bot RPC configuration") {
		t.Fatalf("partial configuration startup = %v, %v", impl, err)
	}
}

func TestAgentDatabaseRejectsInvalidDSNWithoutEchoingIt(t *testing.T) {
	dsn := "private-invalid-dsn"
	_, err := openAgentDatabase(dsn)
	if err == nil || strings.Contains(err.Error(), dsn) {
		t.Fatalf("invalid DSN error = %v", err)
	}
}

func TestAgentStartupRejectsInvalidSnowflakeNode(t *testing.T) {
	t.Setenv("AGENT_SNOWFLAKE_NODE_ID", "not-a-number")
	impl, closeClients, err := newAgentServer(context.Background(), "127.0.0.1:9002", "127.0.0.1:9003", "127.0.0.1:9001", &gorm.DB{})
	if impl != nil || closeClients != nil || err == nil || !strings.Contains(err.Error(), "AGENT_SNOWFLAKE_NODE_ID") {
		t.Fatalf("invalid node startup = %v, %v", impl, err)
	}
}

func TestAgentStartupConnectsDraftReadToUserDatabaseAndIM(t *testing.T) {
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT r.team_id")).WithArgs(int64(9001), int64(400)).
		WillReturnRows(sqlmock.NewRows([]string{"team_id", "group_id", "initiator_id", "status", "title", "description", "assignee_id", "due_at_unix_ms", "source_message_id", "revision"}).
			AddRow(200, 300, 400, "waiting_confirmation", "修复缓存", "复核", 500, 1000, 600, 1))
	impl, closeClients, err := newAgentServer(context.Background(), imAddr, "127.0.0.1:9003", userAddr, db)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClients()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer user-token"))
	resp, err := impl.GetTaskDraft(ctx, &agentpb.GetTaskDraftRequest{RunId: 9001})
	if err != nil || resp.GetDraft().GetTitle() != "修复缓存" || resp.GetStatus() != "waiting_confirmation" {
		t.Fatalf("read configured draft = %v, %v", resp, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
