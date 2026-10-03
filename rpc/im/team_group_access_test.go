package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestCheckTeamGroupAccessOverRPC(t *testing.T) {
	s, mock := testIMServer(t)
	token := "Bearer " + validIMToken(t)
	s.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != token {
			t.Errorf("team member check did not preserve original authorization: %v", req)
			return status.Error(codes.Unauthenticated, "unexpected forwarded authorization")
		}
		return nil
	})
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	mock.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
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
	result, err := pb.NewIMClient(conn).CheckTeamGroupAccess(ctx, &pb.CheckTeamGroupAccessRequest{TeamId: 200, GroupId: 300})
	if err != nil || result == nil {
		t.Fatalf("access = %v, %v", result, err)
	}
}

func TestCheckTeamGroupAccessDeniesFormerMembers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		memberTeam any
		teamErr    error
	}{
		{"left group", nil, nil},
		{"left team", int64(200), status.Error(codes.PermissionDenied, "left team")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return tc.teamErr })
			rows := sqlmock.NewRows([]string{"team_id"})
			if tc.memberTeam != nil {
				rows.AddRow(tc.memberTeam)
			}
			mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(rows)
			result, err := s.CheckTeamGroupAccess(historyContext(t), &pb.CheckTeamGroupAccessRequest{TeamId: 200, GroupId: 300})
			if result != nil || status.Code(err) != codes.PermissionDenied {
				t.Fatalf("access = %v, %v", result, err)
			}
		})
	}
}

func TestCheckTeamGroupAccessRejectsWrongScope(t *testing.T) {
	for _, groupTeam := range []any{nil, int64(201)} {
		s, mock := testIMServer(t)
		s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
		mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(groupTeam))
		mock.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(groupTeam))
		result, err := s.CheckTeamGroupAccess(historyContext(t), &pb.CheckTeamGroupAccessRequest{TeamId: 200, GroupId: 300})
		if result != nil || status.Code(err) != codes.NotFound {
			t.Fatalf("group team %v: %v, %v", groupTeam, result, err)
		}
	}
}

func TestCheckTeamGroupAccessRejectsInvalidInputAndDatabaseFailure(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	for _, req := range []*pb.CheckTeamGroupAccessRequest{nil, {TeamId: 0, GroupId: 300}, {TeamId: 200, GroupId: 0}} {
		result, err := s.CheckTeamGroupAccess(historyContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid input = %v, %v", result, err)
		}
	}
	result, err := s.CheckTeamGroupAccess(context.Background(), &pb.CheckTeamGroupAccessRequest{TeamId: 200, GroupId: 300})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token = %v, %v", result, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	mock.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).
		WillReturnError(errors.New("private database detail"))
	result, err = s.CheckTeamGroupAccess(historyContext(t), &pb.CheckTeamGroupAccessRequest{TeamId: 200, GroupId: 300})
	if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private database detail") {
		t.Fatalf("database failure = %v, %v", result, err)
	}
}
