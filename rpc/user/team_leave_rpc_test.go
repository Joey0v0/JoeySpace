package main

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetTeamLeaveOperationReturnsOwnPendingAndCompleted(t *testing.T) {
	for _, state := range []int8{0, 1} {
		s, mock := newTestUserServer(t)
		expectLeaveProfile(mock)
		expectStoredLeave(mock, state)
		got, err := s.GetTeamLeaveOperation(leaveActorContext(t), &pb.GetTeamLeaveOperationRequest{TeamId: 100, RequestKey: "same-key"})
		if err != nil || got.GetOperationId() != 89 || got.GetTeamId() != 100 || got.GetGeneration() != 7 || got.GetStatus() != int32(state) {
			t.Fatalf("state=%d response=%v error=%v", state, got, err)
		}
	}
}

func TestGetTeamLeaveOperationRequiresLoginBeforeDatabaseRead(t *testing.T) {
	s, _ := newTestUserServer(t)
	_, err := s.GetTeamLeaveOperation(context.Background(), &pb.GetTeamLeaveOperationRequest{TeamId: 100, RequestKey: "same-key"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing login: %v", err)
	}
}

func TestGetTeamLeaveOperationRejectsOtherTeamAndMissingKey(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectLeaveProfile(mock)
	expectStoredLeave(mock, 0)
	_, err := s.GetTeamLeaveOperation(leaveActorContext(t), &pb.GetTeamLeaveOperationRequest{TeamId: 101, RequestKey: "same-key"})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("wrong team: %v", err)
	}
	expectLeaveProfile(mock)
	mock.ExpectQuery("^SELECT id, team_id, user_id, request_key, generation, status FROM `user_team_leave_operations`").
		WithArgs(int64(42), "absent-key", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "user_id", "request_key", "generation", "status"}))
	_, err = s.GetTeamLeaveOperation(leaveActorContext(t), &pb.GetTeamLeaveOperationRequest{TeamId: 100, RequestKey: "absent-key"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("missing key: %v", err)
	}
}

func TestLeaveTeamNoIMConfigurationDoesNotStartOperation(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectLeaveProfile(mock)
	mock.ExpectQuery("^SELECT id, team_id, user_id, request_key, generation, status FROM `user_team_leave_operations`").
		WithArgs(int64(42), "new-key", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "user_id", "request_key", "generation", "status"}))
	_, err := s.LeaveTeam(leaveActorContext(t), &pb.LeaveTeamRequest{TeamId: 100, RequestKey: "new-key"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("missing IM config: %v", err)
	}
}

func TestLeaveTeamCompletedRetryNeedsNoIMOrCurrentMembership(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectLeaveProfile(mock)
	expectStoredLeave(mock, 1)
	got, err := s.LeaveTeam(leaveActorContext(t), &pb.LeaveTeamRequest{TeamId: 100, RequestKey: "same-key"})
	if err != nil || got.GetStatus() != 1 || got.GetGeneration() != 7 {
		t.Fatalf("completed replay: %v, %v", got, err)
	}
}
