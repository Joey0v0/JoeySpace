package main

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/push"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The IM cleanup can fail after User has revoked membership. Push must then
// observe the revoked state over its real private mTLS connection, and a retry
// must complete the same operation without reopening delivery.
func TestTeamLeaveCleanupFailureKeepsPushRevokedThroughRetry(t *testing.T) {
	users, mock := newTestUserServer(t)
	im := &fakeLeaveIM{err: status.Error(codes.Unavailable, "IM cleanup unavailable")}
	users.leaveIM = im
	serverFiles, pushFiles, _ := pushListenerCertificates(t)
	runtime, err := newUserPushRuntime(userPushConfig{ListenOn: "127.0.0.1:0", Files: serverFiles}, users)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Stop)
	go func() { _ = runtime.server.Serve(runtime.listener) }()
	eligibility, err := push.NewTeamEligibilityClient(push.TeamEligibilityClientConfig{Addr: runtime.listener.Addr().String(), Files: pushFiles})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = eligibility.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// A saved pending operation models the committed User revocation. IM has
	// not yet acknowledged cleanup, so old IM group rows may still exist.
	expectLeaveProfile(mock)
	expectStoredLeave(mock, 0)
	expectReadableLeaveMember(mock, model.TeamMembershipLeaving, 7)
	if _, err := users.LeaveTeam(leaveActorContext(t), &pb.LeaveTeamRequest{TeamId: 100, RequestKey: "same-key"}); status.Code(err) != codes.Unavailable || im.calls != 1 {
		t.Fatalf("failed cleanup: calls=%d err=%v", im.calls, err)
	}
	expectLeaveProfile(mock)
	expectStoredLeave(mock, 0)
	if op, err := users.GetTeamLeaveOperation(leaveActorContext(t), &pb.GetTeamLeaveOperationRequest{TeamId: 100, RequestKey: "same-key"}); err != nil || op.GetStatus() != 0 || op.GetGeneration() != 7 {
		t.Fatalf("pending operation: %v %v", op, err)
	}
	expectPushTeamQuery(mock, sqlmock.NewRows([]string{"status", "generation"}))
	if generation, allowed, err := eligibility.CheckCurrentTeamMember(ctx, 100, 42); err != nil || allowed || generation != 0 {
		t.Fatalf("Push during pending cleanup: generation=%d allowed=%t err=%v", generation, allowed, err)
	}

	// The same request key retries IM cleanup and finishes the User operation.
	im.err = nil
	expectLeaveProfile(mock)
	expectStoredLeave(mock, 0)
	expectReadableLeaveMember(mock, model.TeamMembershipLeaving, 7)
	mock.ExpectBegin()
	expectLockedLeaveRows(mock, 0, model.TeamMembershipLeaving, 7)
	mock.ExpectExec("^UPDATE `team_members` SET `membership_state`=\\? WHERE team_id = \\? AND user_id = \\? AND generation = \\? AND membership_state = \\?$").
		WithArgs(model.TeamMembershipLeft, int64(100), int64(42), int64(7), model.TeamMembershipLeaving).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("^UPDATE `user_team_leave_operations` SET ").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	op, err := users.LeaveTeam(leaveActorContext(t), &pb.LeaveTeamRequest{TeamId: 100, RequestKey: "same-key"})
	if err != nil || op.GetStatus() != 1 || op.GetGeneration() != 7 || im.calls != 2 {
		t.Fatalf("completed retry: operation=%v calls=%d err=%v", op, im.calls, err)
	}
	expectPushTeamQuery(mock, sqlmock.NewRows([]string{"status", "generation"}))
	if generation, allowed, err := eligibility.CheckCurrentTeamMember(ctx, 100, 42); err != nil || allowed || generation != 0 {
		t.Fatalf("Push after cleanup: generation=%d allowed=%t err=%v", generation, allowed, err)
	}

	// A storage failure is not an explicit denial: Push must retry delivery
	// rather than silently treating an unknown membership as revoked.
	mock.ExpectQuery(regexp.QuoteMeta(pushTeamQuery)).WithArgs(int64(100), int64(42), model.TeamMembershipActive, 2).
		WillReturnError(context.DeadlineExceeded)
	if _, allowed, err := eligibility.CheckCurrentTeamMember(ctx, 100, 42); allowed || status.Code(err) != codes.Unavailable {
		t.Fatalf("Push on unknown eligibility: allowed=%t err=%v", allowed, err)
	}
}
