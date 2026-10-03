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

func expectTeamMembership(mock sqlmock.Sqlmock, present bool, role int8) {
	rows := sqlmock.NewRows([]string{"user_id", "role"})
	if present {
		rows.AddRow(int64(42), role)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT user_id, role FROM `team_members`")).WithArgs(int64(100), int64(42), 1).
		WillReturnRows(rows)
}

func TestCheckTeamMemberOverRPC(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectTeamMembership(mock, true, 2)

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
	result, err := pb.NewUserClient(conn).CheckTeamMember(ctx, &pb.CheckTeamMemberRequest{TeamId: 100})
	if err != nil || result == nil || result.GetUserId() != 42 || result.GetRole() != 2 {
		t.Fatalf("check team member RPC: %v, %v", result, err)
	}
}

func TestCheckTeamMemberReturnsVerifiedRole(t *testing.T) {
	for _, role := range []int8{0, 1} {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectTeamMembership(mock, true, role)
		result, err := s.CheckTeamMember(teamContext(t), &pb.CheckTeamMemberRequest{TeamId: 100})
		if err != nil || result == nil || result.GetUserId() != 42 || result.GetRole() != int32(role) {
			t.Fatalf("role %d: %v, %v", role, result, err)
		}
	}
}

func TestCheckTeamMemberRejectsInvalidInputAndToken(t *testing.T) {
	s, _ := newTestUserServer(t)
	for _, req := range []*pb.CheckTeamMemberRequest{nil, {TeamId: 0}, {TeamId: -1}} {
		result, err := s.CheckTeamMember(teamContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %v, %v", result, err)
		}
	}
	result, err := s.CheckTeamMember(context.Background(), &pb.CheckTeamMemberRequest{TeamId: 100})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
}

func TestCheckTeamMemberRejectsNonMemberAndDatabaseFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		queryErr error
		wantCode codes.Code
	}{
		{"non-member", nil, codes.PermissionDenied},
		{"database failure", errors.New("database unavailable"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectTeamCreator(mock)
			if tc.queryErr == nil {
				expectTeamMembership(mock, false, 0)
			} else {
				mock.ExpectQuery(regexp.QuoteMeta("SELECT user_id, role FROM `team_members`")).WithArgs(int64(100), int64(42), 1).
					WillReturnError(tc.queryErr)
			}
			result, err := s.CheckTeamMember(teamContext(t), &pb.CheckTeamMemberRequest{TeamId: 100})
			if result != nil || status.Code(err) != tc.wantCode {
				t.Fatalf("membership check: %v, %v", result, err)
			}
		})
	}
}
