package main

import (
	"context"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func expectListMembership(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `user_id` FROM `team_members`")).WithArgs(int64(100), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(42))
}

func TestListTeamMembersRejectsInvalidInputAndToken(t *testing.T) {
	s, _ := newTestUserServer(t)
	for _, req := range []*pb.ListTeamMembersRequest{
		{TeamId: 0}, {TeamId: 100, AfterUserId: -1}, {TeamId: 100, Limit: -1}, {TeamId: 100, Limit: 101},
	} {
		result, err := s.ListTeamMembers(teamContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid list request: %v, %v", result, err)
		}
	}
	result, err := s.ListTeamMembers(context.Background(), &pb.ListTeamMembersRequest{TeamId: 100})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
}

func TestListTeamMembersOverRPC(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectListMembership(mock)
	mock.ExpectQuery("SELECT team_members.user_id AS user_id, users.username, users.nickname, team_members.role FROM `team_members`").
		WithArgs(int64(100), int64(0), 3).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "username", "nickname", "role"}).
			AddRow(5, "bob", "Bob", 0).AddRow(7, "carol", "Carol", 1).AddRow(42, "alice", "Alice", 2))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterUserServer(server, s)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", metadata.ValueFromIncomingContext(teamContext(t), "authorization")[0]))
	result, err := pb.NewUserClient(conn).ListTeamMembers(ctx, &pb.ListTeamMembersRequest{TeamId: 100, Limit: 2})
	if err != nil || len(result.GetMembers()) != 2 || result.GetNextAfterUserId() != 7 || result.GetMembers()[1].GetRole() != 1 {
		t.Fatalf("member list RPC: %v, %v", result, err)
	}
}

func TestListTeamMembersDeniesNonMemberBeforeListing(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `user_id` FROM `team_members`")).WithArgs(int64(100), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}))
	result, err := s.ListTeamMembers(teamContext(t), &pb.ListTeamMembersRequest{TeamId: 100})
	if result != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-member list: %v, %v", result, err)
	}
}

func TestListTeamMembersLastPage(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectListMembership(mock)
	mock.ExpectQuery("SELECT team_members.user_id AS user_id, users.username, users.nickname, team_members.role FROM `team_members`").
		WithArgs(int64(100), int64(7), 3).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "username", "nickname", "role"}).AddRow(42, "alice", "Alice", 2))
	result, err := s.ListTeamMembers(teamContext(t), &pb.ListTeamMembersRequest{TeamId: 100, AfterUserId: 7, Limit: 2})
	if err != nil || len(result.GetMembers()) != 1 || result.GetMembers()[0].GetUserId() != 42 || result.GetNextAfterUserId() != 0 {
		t.Fatalf("last page: %v, %v", result, err)
	}
}
