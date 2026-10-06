package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const imTestSecret = "im-test-secret-not-for-real-use"
const memberQuery = "SELECT groups.team_id FROM `group_members` JOIN groups ON groups.id = group_members.group_id WHERE group_members.group_id = ? AND group_members.user_id = ? LIMIT ?"

func testIMServer(t *testing.T) (*imServer, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return &imServer{db: db, jwtSecret: imTestSecret}, mock
}

func validIMToken(t *testing.T) string {
	t.Helper()
	token, err := pkgjwt.GenerateToken(42, imTestSecret, 1)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestCheckGroupMemberRejectsInvalidRequestBeforeQuery(t *testing.T) {
	s, _ := testIMServer(t)
	token := validIMToken(t)
	wrongSignature, err := pkgjwt.GenerateToken(42, "wrong-secret", 1)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := pkgjwt.GenerateToken(42, imTestSecret, -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		groupID int64
		headers []string
		code    codes.Code
	}{
		{0, []string{"Bearer " + token}, codes.InvalidArgument},
		{100, nil, codes.Unauthenticated},
		{100, []string{"Bearer invalid"}, codes.Unauthenticated},
		{100, []string{"Bearer " + wrongSignature}, codes.Unauthenticated},
		{100, []string{"Bearer " + expired}, codes.Unauthenticated},
		{100, []string{"Bearer " + token, "Bearer " + token}, codes.Unauthenticated},
	} {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"authorization": tc.headers})
		result, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: tc.groupID})
		if result != nil || status.Code(err) != tc.code {
			t.Fatalf("group %d: %v, %v", tc.groupID, result, err)
		}
	}
}

func TestCheckGroupMemberOverRPC(t *testing.T) {
	s, mock := testIMServer(t)
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(100), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(nil))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterIMServer(server, s)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+validIMToken(t), "user_id", "99"))
	result, err := pb.NewIMClient(conn).CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
	if err != nil || result == nil {
		t.Fatalf("membership RPC: %v, %v", result, err)
	}
}

func TestCheckGroupMemberDatabaseOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
		err  error
		code codes.Code
	}{
		{"non-member", sqlmock.NewRows([]string{"team_id"}), nil, codes.PermissionDenied},
		{"database failure", nil, errors.New("database disconnected"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			expect := mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(100), int64(42), 1)
			if tc.err != nil {
				expect.WillReturnError(tc.err)
			} else {
				expect.WillReturnRows(tc.rows)
			}
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
			result, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
			if result != nil || status.Code(err) != tc.code {
				t.Fatalf("membership query: %v, %v", result, err)
			}
		})
	}
}

type teamCheckFunc func(context.Context, *userpb.CheckTeamMemberRequest) error

func (f teamCheckFunc) CheckTeamMember(ctx context.Context, req *userpb.CheckTeamMemberRequest, _ ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	if err := f(ctx, req); err != nil {
		return nil, err
	}
	return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: 1}, nil
}

func (teamCheckFunc) AuthorizeTeamGroupCreation(context.Context, *userpb.AuthorizeTeamGroupCreationRequest, ...grpc.CallOption) (*userpb.AuthorizeTeamGroupCreationResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by membership tests")
}

func TestCheckGroupMemberRequiresTeamMembershipForTeamGroup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rpcErr error
		want   codes.Code
	}{
		{"current team member", nil, codes.OK},
		{"left team", status.Error(codes.PermissionDenied, "not a member"), codes.PermissionDenied},
		{"team RPC unavailable", status.Error(codes.Unavailable, "unavailable"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(100), int64(42), 1).
				WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
			called := false
			token := "Bearer " + validIMToken(t)
			s.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
				called = true
				md, _ := metadata.FromOutgoingContext(ctx)
				if req.GetTeamId() != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != token {
					t.Errorf("wrong team or token forwarded: team=%d, authorization=%v", req.GetTeamId(), md.Get("authorization"))
				}
				return tc.rpcErr
			})
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", token))
			result, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
			if !called || status.Code(err) != tc.want || (tc.want == codes.OK) != (result != nil) {
				t.Fatalf("team group check: called=%t, result=%v, err=%v", called, result, err)
			}
		})
	}
}

func TestCheckGroupMemberTeamGroupFailsWithoutTeamClient(t *testing.T) {
	s, mock := testIMServer(t)
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(100), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
	result, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
	if result != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("missing team RPC client: %v, %v", result, err)
	}
}
