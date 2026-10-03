package main

import (
	"context"
	"errors"
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

func expectTargetTeamMember(mock sqlmock.Sqlmock, targetStatus *int8, queryErr error) {
	query := mock.ExpectQuery(regexp.QuoteMeta("SELECT users.status FROM `team_members` JOIN users ON users.id = team_members.user_id"))
	query.WithArgs(int64(100), int64(77), 1)
	if queryErr != nil {
		query.WillReturnError(queryErr)
		return
	}
	rows := sqlmock.NewRows([]string{"status"})
	if targetStatus != nil {
		rows.AddRow(*targetStatus)
	}
	query.WillReturnRows(rows)
}

func TestCheckTeamMemberByIDOverRPC(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectTeamMembership(mock, true, 0)
	active := int8(1)
	expectTargetTeamMember(mock, &active, nil)

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
	result, err := pb.NewUserClient(conn).CheckTeamMemberByID(ctx, &pb.CheckTeamMemberByIDRequest{TeamId: 100, UserId: 77})
	if err != nil || result == nil {
		t.Fatalf("check target team member RPC: %v, %v", result, err)
	}
}

func TestCheckTeamMemberByIDRejectsInvalidRequestAndToken(t *testing.T) {
	s, _ := newTestUserServer(t)
	for _, req := range []*pb.CheckTeamMemberByIDRequest{nil, {TeamId: 0, UserId: 77}, {TeamId: 100, UserId: 0}} {
		result, err := s.CheckTeamMemberByID(teamContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid target request: %v, %v", result, err)
		}
	}
	result, err := s.CheckTeamMemberByID(context.Background(), &pb.CheckTeamMemberByIDRequest{TeamId: 100, UserId: 77})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
}

func TestCheckTeamMemberByIDRejectsCallerOutsideTeam(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectTeamMembership(mock, false, 0)
	result, err := s.CheckTeamMemberByID(teamContext(t), &pb.CheckTeamMemberByIDRequest{TeamId: 100, UserId: 77})
	if result != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("caller outside team: %v, %v", result, err)
	}
}

func TestCheckTeamMemberByIDRejectsUnavailableTarget(t *testing.T) {
	disabled := int8(0)
	for _, tc := range []struct {
		name     string
		status   *int8
		queryErr error
		wantCode codes.Code
	}{
		{"not in team", nil, nil, codes.NotFound},
		{"disabled", &disabled, nil, codes.FailedPrecondition},
		{"database failure", nil, errors.New("database unavailable"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectTeamCreator(mock)
			expectTeamMembership(mock, true, 0)
			expectTargetTeamMember(mock, tc.status, tc.queryErr)
			result, err := s.CheckTeamMemberByID(teamContext(t), &pb.CheckTeamMemberByIDRequest{TeamId: 100, UserId: 77})
			if result != nil || status.Code(err) != tc.wantCode {
				t.Fatalf("target %s: %v, %v", tc.name, result, err)
			}
		})
	}
}
