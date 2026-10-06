package main

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeLeaveIM struct {
	calls                  int
	team, user, generation int64
	err                    error
}

func (f *fakeLeaveIM) CloseTeamGroupMemberships(_ context.Context, team, user, generation int64) error {
	f.calls++
	f.team, f.user, f.generation = team, user, generation
	return f.err
}

func leaveActorContext(t *testing.T) context.Context {
	t.Helper()
	token, err := pkgjwt.GenerateToken(42, profileTestSecret, 1)
	if err != nil {
		t.Fatal(err)
	}
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token, "user_id", "900"))
}

func expectLeaveProfile(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("^"+regexp.QuoteMeta(profileQuery)+"$").WithArgs(int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 1))
}

func expectStoredLeave(mock sqlmock.Sqlmock, state int8) {
	mock.ExpectQuery("^SELECT id, team_id, user_id, request_key, generation, status FROM `user_team_leave_operations` WHERE user_id = \\? AND request_key = \\? LIMIT \\?$").
		WithArgs(int64(42), "same-key", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "user_id", "request_key", "generation", "status"}).AddRow(89, 100, 42, "same-key", 7, state))
}

func expectReadableLeaveMember(mock sqlmock.Sqlmock, state int8, generation int64) {
	mock.ExpectQuery("^SELECT role, membership_state, generation FROM `team_members` WHERE team_id = \\? AND user_id = \\? LIMIT \\?$").
		WithArgs(int64(100), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "membership_state", "generation"}).AddRow(0, state, generation))
}

func expectLockedLeaveRows(mock sqlmock.Sqlmock, opStatus int8, memberState int8, generation int64) {
	mock.ExpectQuery("^"+regexp.QuoteMeta(leaveMemberSQL)+"$").WithArgs(int64(100), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "membership_state", "generation"}).AddRow(0, memberState, generation))
	mock.ExpectQuery("^SELECT id, team_id, user_id, request_key, generation, status FROM `user_team_leave_operations` WHERE id = \\? LIMIT \\? FOR UPDATE$").
		WithArgs(int64(89), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "user_id", "request_key", "generation", "status"}).AddRow(89, 100, 42, "same-key", 7, opStatus))
}

func TestCompleteOwnTeamLeaveUsesFixedOperationAndFinishesAfterIM(t *testing.T) {
	s, mock := newTestUserServer(t)
	im := &fakeLeaveIM{}
	s.leaveIM = im
	expectLeaveProfile(mock)
	expectStoredLeave(mock, 0)
	expectReadableLeaveMember(mock, model.TeamMembershipLeaving, 7)
	mock.ExpectBegin()
	expectLockedLeaveRows(mock, 0, model.TeamMembershipLeaving, 7)
	mock.ExpectExec("^UPDATE `team_members` SET `membership_state`=\\? WHERE team_id = \\? AND user_id = \\? AND generation = \\? AND membership_state = \\?$").
		WithArgs(model.TeamMembershipLeft, int64(100), int64(42), int64(7), model.TeamMembershipLeaving).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("^UPDATE `user_team_leave_operations` SET ").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	got, err := s.completeOwnTeamLeave(leaveActorContext(t), 100, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != 1 || got.Generation != 7 || im.calls != 1 || im.team != 100 || im.user != 42 || im.generation != 7 {
		t.Fatalf("unexpected result %#v and IM call %#v", got, im)
	}
}

func TestCompleteOwnTeamLeaveRemoteFailureKeepsPending(t *testing.T) {
	s, mock := newTestUserServer(t)
	im := &fakeLeaveIM{err: status.Error(codes.Unavailable, "remote failed")}
	s.leaveIM = im
	expectLeaveProfile(mock)
	expectStoredLeave(mock, 0)
	expectReadableLeaveMember(mock, model.TeamMembershipLeaving, 7)
	if _, err := s.completeOwnTeamLeave(leaveActorContext(t), 100, "same-key"); status.Code(err) != codes.Unavailable {
		t.Fatalf("unexpected error: %v", err)
	}
	if im.calls != 1 {
		t.Fatal("IM must be called once")
	}
}

func TestCompleteOwnTeamLeaveCompletedRetryAfterRejoinSkipsIM(t *testing.T) {
	s, mock := newTestUserServer(t)
	im := &fakeLeaveIM{err: errors.New("must not call")}
	s.leaveIM = im
	expectLeaveProfile(mock)
	expectStoredLeave(mock, 1)
	got, err := s.completeOwnTeamLeave(leaveActorContext(t), 100, "same-key")
	if err != nil || got.Status != 1 || im.calls != 0 {
		t.Fatalf("unexpected replay %#v, %v, calls=%d", got, err, im.calls)
	}
}

func TestFinishTeamLeaveIntentRejectsNewGeneration(t *testing.T) {
	s, mock := newTestUserServer(t)
	mock.ExpectBegin()
	expectLockedLeaveRows(mock, 0, model.TeamMembershipActive, 8)
	mock.ExpectRollback()
	_, err := finishTeamLeaveIntent(context.Background(), s.db, &teamLeaveIntent{ID: 89, RequestKey: "same-key", TeamID: 100, UserID: 42, Generation: 7})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTeamLeaveIMClientRejectsPartialConfiguration(t *testing.T) {
	_, err := loadTeamLeaveIMConfig(func(k string) string {
		if k == "USER_LEAVE_IM_RPC_ADDR" {
			return "im:9094"
		}
		return ""
	})
	if err == nil {
		t.Fatal("partial mTLS configuration accepted")
	}
}

type fakeLeaveRPC struct {
	response *impb.CloseTeamGroupMembershipsResponse
	err      error
	request  *impb.CloseTeamGroupMembershipsRequest
	metadata metadata.MD
}

func (f *fakeLeaveRPC) CloseTeamGroupMemberships(ctx context.Context, req *impb.CloseTeamGroupMembershipsRequest, _ ...grpc.CallOption) (*impb.CloseTeamGroupMembershipsResponse, error) {
	f.request = req
	f.metadata, _ = metadata.FromOutgoingContext(ctx)
	return f.response, f.err
}

func TestTeamLeaveIMClientRejectsInvalidAckAndStripsToken(t *testing.T) {
	for _, tc := range []struct {
		name string
		ack  int64
		want codes.Code
	}{
		{"exact generation", 7, codes.OK},
		{"already closed newer generation", 8, codes.OK},
		{"stale generation", 6, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc := &fakeLeaveRPC{response: &impb.CloseTeamGroupMembershipsResponse{ClosedThroughGeneration: tc.ack}}
			client := &teamLeaveIMClient{rpc: rpc}
			ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer must-not-forward"))
			err := client.CloseTeamGroupMemberships(ctx, 100, 42, 7)
			if status.Code(err) != tc.want || rpc.request.GetTeamId() != 100 || rpc.request.GetUserId() != 42 || rpc.request.GetGeneration() != 7 || len(rpc.metadata) != 0 {
				t.Fatalf("result=%v request=%v metadata=%v", err, rpc.request, rpc.metadata)
			}
		})
	}
}

func TestFinishTeamLeaveIntentRollsBackIfOperationUpdateFails(t *testing.T) {
	s, mock := newTestUserServer(t)
	mock.ExpectBegin()
	expectLockedLeaveRows(mock, 0, model.TeamMembershipLeaving, 7)
	mock.ExpectExec("^UPDATE `team_members` SET `membership_state`=\\? WHERE team_id = \\? AND user_id = \\? AND generation = \\? AND membership_state = \\?$").
		WithArgs(model.TeamMembershipLeft, int64(100), int64(42), int64(7), model.TeamMembershipLeaving).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("^UPDATE `user_team_leave_operations` SET ").WillReturnError(errors.New("disk failure"))
	mock.ExpectRollback()
	_, err := finishTeamLeaveIntent(context.Background(), s.db, &teamLeaveIntent{ID: 89, RequestKey: "same-key", TeamID: 100, UserID: 42, Generation: 7})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("unexpected error: %v", err)
	}
}
