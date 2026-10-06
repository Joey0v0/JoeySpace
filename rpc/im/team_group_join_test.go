package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestJoinTeamGroupWritesOnlyCurrentMember(t *testing.T) {
	s, mock := testIMServer(t)
	token := "Bearer " + validIMToken(t)
	s.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != token {
			t.Fatalf("wrong team authorization: %v %v", req, md)
		}
		return nil
	})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `id` FROM `groups` WHERE id = ? AND team_id = ? LIMIT ?")).
		WithArgs(int64(300), int64(200), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(300)))
	mock.ExpectBegin()
	fenceTestLock(mock, 0)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `group_members`")).
		WithArgs(int64(300), int64(42), int8(0)).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
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
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", token))
	result, err := pb.NewIMClient(conn).JoinTeamGroup(ctx, &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
	if err != nil || result == nil {
		t.Fatalf("join: %v %v", result, err)
	}
}

func TestJoinTeamGroupRejectsBeforeWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		teamErr error
		want    codes.Code
	}{
		{"non-member", status.Error(codes.PermissionDenied, "not a member"), codes.PermissionDenied},
		{"unauthenticated", status.Error(codes.Unauthenticated, "invalid user token"), codes.Unauthenticated},
		{"canceled", status.Error(codes.Canceled, "request canceled"), codes.Canceled},
		{"deadline", status.Error(codes.DeadlineExceeded, "request expired"), codes.DeadlineExceeded},
		{"team RPC unavailable", status.Error(codes.Unavailable, "private detail"), codes.Unavailable},
		{"unexpected team RPC error", status.Error(codes.Internal, "private detail"), codes.Unavailable},
		{"group outside team or legacy group", nil, codes.NotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return tc.teamErr })
			if tc.teamErr == nil {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT `id` FROM `groups` WHERE id = ? AND team_id = ? LIMIT ?")).
					WithArgs(int64(300), int64(200), 1).WillReturnRows(sqlmock.NewRows([]string{"id"}))
			}
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
			result, err := s.JoinTeamGroup(ctx, &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
			if result != nil || status.Code(err) != tc.want {
				t.Fatalf("join: %v %v", result, err)
			}
			if tc.want == codes.Unavailable && status.Convert(err).Message() != "team membership check unavailable" {
				t.Fatalf("team RPC error was not sanitized before database access: %v", err)
			}
		})
	}
}

func TestJoinTeamGroupDuplicateReturnsSuccess(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `id` FROM `groups` WHERE id = ? AND team_id = ? LIMIT ?")).
		WithArgs(int64(300), int64(200), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(300)))
	mock.ExpectBegin()
	fenceTestLock(mock, 0)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `group_members`")).
		WithArgs(int64(300), int64(42), int8(0)).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate member"})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `group_id` FROM `group_members` WHERE group_id = ? AND user_id = ? LIMIT ?")).
		WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(300)))
	mock.ExpectCommit()
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
	result, err := s.JoinTeamGroup(ctx, &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
	if err != nil || result == nil {
		t.Fatalf("repeated join: %v %v", result, err)
	}
}

func TestJoinTeamGroupRejectsInvalidRequestAndDatabaseFailure(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
	for _, req := range []*pb.JoinTeamGroupRequest{nil, {TeamId: 0, GroupId: 300}, {TeamId: 200, GroupId: -1}} {
		result, err := s.JoinTeamGroup(ctx, req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %v %v", result, err)
		}
	}
	result, err := s.JoinTeamGroup(context.Background(), &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v %v", result, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `id` FROM `groups` WHERE id = ? AND team_id = ? LIMIT ?")).
		WithArgs(int64(300), int64(200), 1).WillReturnError(errors.New("private database detail"))
	result, err = s.JoinTeamGroup(ctx, &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
	if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private database detail" {
		t.Fatalf("database failure: %v %v", result, err)
	}
}
