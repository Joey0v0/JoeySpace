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

func expectMemberRole(mock sqlmock.Sqlmock, userID int64, role int8) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `role` FROM `team_members`")).WithArgs(int64(100), userID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow(role))
}

func expectRoleUpdate(mock sqlmock.Sqlmock, userID int64, role int32, affected int64) {
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE `team_members` SET `role`=").
		WithArgs(role, int64(100), userID, int64(0), int64(1)).
		WillReturnResult(sqlmock.NewResult(0, affected))
	mock.ExpectCommit()
}

func TestSetTeamMemberRoleRejectsInvalidInputAndToken(t *testing.T) {
	s, _ := newTestUserServer(t)
	for _, req := range []*pb.SetTeamMemberRoleRequest{
		{TeamId: 0, UserId: 7, Role: 1},
		{TeamId: 100, UserId: 0, Role: 1},
		{TeamId: 100, UserId: 7, Role: 2},
		{TeamId: 100, UserId: 7, Role: -1},
	} {
		result, err := s.SetTeamMemberRole(teamContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid role request: %v, %v", result, err)
		}
	}
	result, err := s.SetTeamMemberRole(context.Background(), &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 7, Role: 1})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
}

func TestSetTeamMemberRoleOwnerOverRPC(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectMemberRole(mock, 42, 2)
	expectMemberRole(mock, 7, 0)
	expectRoleUpdate(mock, 7, 1, 1)

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
	result, err := pb.NewUserClient(conn).SetTeamMemberRole(ctx, &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 7, Role: 1})
	if err != nil || result == nil {
		t.Fatalf("promote member RPC: %v, %v", result, err)
	}
}

func TestSetTeamMemberRoleRequiresOwner(t *testing.T) {
	for _, role := range []int8{0, 1} {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectMemberRole(mock, 42, role)
		result, err := s.SetTeamMemberRole(teamContext(t), &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 7, Role: 1})
		if result != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("operator role %d: %v, %v", role, result, err)
		}
	}
	t.Run("non-member", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `role` FROM `team_members`")).WithArgs(int64(100), int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"role"}))
		result, err := s.SetTeamMemberRole(teamContext(t), &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 7, Role: 1})
		if result != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("non-member: %v, %v", result, err)
		}
	})
}

func TestSetTeamMemberRoleProtectsOwnerAndMissingTarget(t *testing.T) {
	t.Run("own role", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectMemberRole(mock, 42, 2)
		result, err := s.SetTeamMemberRole(teamContext(t), &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 42, Role: 0})
		if result != nil || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("owner role change: %v, %v", result, err)
		}
	})
	t.Run("missing target", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectMemberRole(mock, 42, 2)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT `role` FROM `team_members`")).WithArgs(int64(100), int64(7), 1).
			WillReturnRows(sqlmock.NewRows([]string{"role"}))
		result, err := s.SetTeamMemberRole(teamContext(t), &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 7, Role: 1})
		if result != nil || status.Code(err) != codes.NotFound {
			t.Fatalf("missing member: %v, %v", result, err)
		}
	})
	t.Run("owner target", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectMemberRole(mock, 42, 2)
		expectMemberRole(mock, 7, 2)
		result, err := s.SetTeamMemberRole(teamContext(t), &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 7, Role: 0})
		if result != nil || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("owner target: %v, %v", result, err)
		}
	})
}

func TestSetTeamMemberRoleDemotesAndDetectsChangedRow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		affected int64
		want     codes.Code
	}{
		{"demote admin", 1, codes.OK},
		{"concurrent change", 0, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectTeamCreator(mock)
			expectMemberRole(mock, 42, 2)
			expectMemberRole(mock, 7, 1)
			expectRoleUpdate(mock, 7, 0, tc.affected)
			result, err := s.SetTeamMemberRole(teamContext(t), &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 7, Role: 0})
			if status.Code(err) != tc.want || (tc.want == codes.OK) != (result != nil) {
				t.Fatalf("demotion: %v, %v", result, err)
			}
		})
	}
}
