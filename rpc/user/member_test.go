package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func expectOperatorRole(mock sqlmock.Sqlmock, role int8) {
	mock.ExpectQuery("^"+regexp.QuoteMeta(activeMemberRoleQuery)+"$").WithArgs(int64(100), int64(42), int64(0), 1).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow(role))
}

func expectTargetStatus(mock sqlmock.Sqlmock, userID int64, status int8) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT `status` FROM `users`")).WithArgs(userID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow(status))
}

const addTargetMemberQuery = "SELECT role, membership_state, generation FROM `team_members` WHERE team_id = ? AND user_id = ? LIMIT ? FOR UPDATE"

func expectAddTargetMember(mock sqlmock.Sqlmock, state, role int8, generation int64, found bool) {
	rows := sqlmock.NewRows([]string{"role", "membership_state", "generation"})
	if found {
		rows.AddRow(role, state, generation)
	}
	mock.ExpectQuery("^"+regexp.QuoteMeta(addTargetMemberQuery)+"$").WithArgs(int64(100), int64(7), 1).WillReturnRows(rows)
}

func TestAddTeamMemberRejectsInvalidInputAndToken(t *testing.T) {
	s, _ := newTestUserServer(t)
	for _, req := range []*pb.AddTeamMemberRequest{{TeamId: 0, UserId: 7}, {TeamId: 100, UserId: 0}} {
		result, err := s.AddTeamMember(teamContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid IDs: %v, %v", result, err)
		}
	}
	result, err := s.AddTeamMember(context.Background(), &pb.AddTeamMemberRequest{TeamId: 100, UserId: 7})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
}

func TestAddTeamMemberOwnerOverRPC(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectOperatorRole(mock, 2)
	expectTargetStatus(mock, 7, 1)
	mock.ExpectBegin()
	expectAddTargetMember(mock, 0, 0, 0, false)
	mock.ExpectExec("INSERT INTO `team_members`").WithArgs(int64(100), int64(7), int64(0), model.TeamMembershipActive, int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

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
	result, err := pb.NewUserClient(conn).AddTeamMember(ctx, &pb.AddTeamMemberRequest{TeamId: 100, UserId: 7})
	if err != nil || result == nil {
		t.Fatalf("add team member RPC: %v, %v", result, err)
	}
}

func TestAddTeamMemberRequiresOwner(t *testing.T) {
	for _, tc := range []struct {
		name          string
		role          int8
		hasMembership bool
	}{
		{name: "ordinary member", role: 0, hasMembership: true},
		{name: "admin", role: 1, hasMembership: true},
		{name: "missing membership"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectTeamCreator(mock)
			if !tc.hasMembership {
				mock.ExpectQuery("^"+regexp.QuoteMeta(activeMemberRoleQuery)+"$").WithArgs(int64(100), int64(42), int64(0), 1).
					WillReturnRows(sqlmock.NewRows([]string{"role"}))
			} else {
				expectOperatorRole(mock, tc.role)
			}
			result, err := s.AddTeamMember(teamContext(t), &pb.AddTeamMemberRequest{TeamId: 100, UserId: 7})
			if result != nil || status.Code(err) != codes.PermissionDenied {
				t.Fatalf("unauthorized add: %v, %v", result, err)
			}
		})
	}
}

func TestAddTeamMemberRejectsDisabledAndDuplicate(t *testing.T) {
	t.Run("disabled target", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectOperatorRole(mock, 2)
		expectTargetStatus(mock, 7, 2)
		result, err := s.AddTeamMember(teamContext(t), &pb.AddTeamMemberRequest{TeamId: 100, UserId: 7})
		if result != nil || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("disabled target: %v, %v", result, err)
		}
	})
	t.Run("duplicate member", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		expectOperatorRole(mock, 2)
		expectTargetStatus(mock, 7, 1)
		mock.ExpectBegin()
		expectAddTargetMember(mock, 0, 0, 0, false)
		mock.ExpectExec("INSERT INTO `team_members`").WithArgs(int64(100), int64(7), int64(0), model.TeamMembershipActive, int64(1)).
			WillReturnError(&driver.MySQLError{Number: 1062, Message: "Duplicate entry"})
		mock.ExpectRollback()
		result, err := s.AddTeamMember(teamContext(t), &pb.AddTeamMemberRequest{TeamId: 100, UserId: 7})
		if result != nil || status.Code(err) != codes.AlreadyExists {
			t.Fatalf("duplicate member: %v, %v", result, err)
		}
	})
	t.Run("database failure", func(t *testing.T) {
		s, mock := newTestUserServer(t)
		expectTeamCreator(mock)
		mock.ExpectQuery("^"+regexp.QuoteMeta(activeMemberRoleQuery)+"$").WithArgs(int64(100), int64(42), int64(0), 1).
			WillReturnError(errors.New("database disconnected"))
		result, err := s.AddTeamMember(teamContext(t), &pb.AddTeamMemberRequest{TeamId: 100, UserId: 7})
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("database failure: %v, %v", result, err)
		}
	})
}

func expectCompletedLeaveForAdd(mock sqlmock.Sqlmock, status int8, found bool) {
	rows := sqlmock.NewRows([]string{"status"})
	if found {
		rows.AddRow(status)
	}
	mock.ExpectQuery("^SELECT `status` FROM `user_team_leave_operations` WHERE team_id = \\? AND user_id = \\? AND generation = \\? LIMIT \\?$").
		WithArgs(int64(100), int64(7), int64(7), 1).WillReturnRows(rows)
}

func TestAddTeamMemberRejoinsOnlyAfterCompletedCleanup(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectOperatorRole(mock, 2)
	expectTargetStatus(mock, 7, 1)
	mock.ExpectBegin()
	expectAddTargetMember(mock, model.TeamMembershipLeft, 1, 7, true)
	expectCompletedLeaveForAdd(mock, 1, true)
	mock.ExpectExec("^UPDATE `team_members` SET ").
		WithArgs(int64(8), model.TeamMembershipActive, int8(0), int64(100), int64(7), model.TeamMembershipLeft, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	got, err := s.AddTeamMember(teamContext(t), &pb.AddTeamMemberRequest{TeamId: 100, UserId: 7})
	if err != nil || got == nil {
		t.Fatalf("completed rejoin: %v, %v", got, err)
	}
}

func TestAddTeamMemberRejectsPendingOrUnprovenRejoin(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      int8
		opStatus   int8
		opFound    bool
		wantCode   codes.Code
		generation int64
	}{
		{"already active", model.TeamMembershipActive, 0, false, codes.AlreadyExists, 7},
		{"cleanup pending", model.TeamMembershipLeaving, 0, false, codes.FailedPrecondition, 7},
		{"left without operation", model.TeamMembershipLeft, 0, false, codes.FailedPrecondition, 7},
		{"left with pending operation", model.TeamMembershipLeft, 0, true, codes.FailedPrecondition, 7},
		{"generation exhausted", model.TeamMembershipLeft, 1, false, codes.FailedPrecondition, int64(^uint64(0) >> 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectTeamCreator(mock)
			expectOperatorRole(mock, 2)
			expectTargetStatus(mock, 7, 1)
			mock.ExpectBegin()
			expectAddTargetMember(mock, tc.state, 0, tc.generation, true)
			if tc.state == model.TeamMembershipLeft && tc.generation == 7 {
				expectCompletedLeaveForAdd(mock, tc.opStatus, tc.opFound)
			}
			mock.ExpectRollback()
			got, err := s.AddTeamMember(teamContext(t), &pb.AddTeamMemberRequest{TeamId: 100, UserId: 7})
			if got != nil || status.Code(err) != tc.wantCode {
				t.Fatalf("unexpected rejoin: %v, %v", got, err)
			}
		})
	}
}

func TestAddTeamMemberRejoinUpdateFailureRollsBack(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectOperatorRole(mock, 2)
	expectTargetStatus(mock, 7, 1)
	mock.ExpectBegin()
	expectAddTargetMember(mock, model.TeamMembershipLeft, 0, 7, true)
	expectCompletedLeaveForAdd(mock, 1, true)
	mock.ExpectExec("^UPDATE `team_members` SET ").WillReturnError(errors.New("write failed"))
	mock.ExpectRollback()
	got, err := s.AddTeamMember(teamContext(t), &pb.AddTeamMemberRequest{TeamId: 100, UserId: 7})
	if got != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("write failure: %v, %v", got, err)
	}
}
