package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	leaveMemberSQL    = "SELECT role, membership_state, generation FROM `team_members` WHERE team_id = ? AND user_id = ? LIMIT ? FOR UPDATE"
	leaveTeamSQL      = "SELECT owner_id FROM `teams` WHERE id = ? LIMIT ?"
	leaveOperationSQL = "SELECT id, team_id, user_id, generation, status FROM `user_team_leave_operations` WHERE user_id = ? AND request_key = ? LIMIT ?"
	leaveUpdateSQL    = "UPDATE `team_members` SET `membership_state`=? WHERE team_id = ? AND user_id = ? AND membership_state = ? AND generation = ? AND role IN (?,?)"
)

func expectLeaveMember(mock sqlmock.Sqlmock, role, state int8, generation int64) {
	mock.ExpectQuery("^"+regexp.QuoteMeta(leaveMemberSQL)+"$").
		WithArgs(int64(100), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "membership_state", "generation"}).AddRow(role, state, generation))
}

func expectLeaveOperation(mock sqlmock.Sqlmock, key string, rows *sqlmock.Rows) {
	mock.ExpectQuery("^"+regexp.QuoteMeta(leaveOperationSQL)+"$").
		WithArgs(int64(42), key, 1).WillReturnRows(rows)
}

func expectLeaveTeam(mock sqlmock.Sqlmock, ownerID int64) {
	mock.ExpectQuery("^"+regexp.QuoteMeta(leaveTeamSQL)+"$").
		WithArgs(int64(100), 1).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(ownerID))
}

func emptyLeaveOperations() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "team_id", "user_id", "generation", "status"})
}

func expectLeaveUpdate(mock sqlmock.Sqlmock, generation, affected int64) {
	mock.ExpectExec("^"+regexp.QuoteMeta(leaveUpdateSQL)+"$").
		WithArgs(model.TeamMembershipLeaving, int64(100), int64(42), model.TeamMembershipActive, generation, int8(0), int8(1)).
		WillReturnResult(sqlmock.NewResult(0, affected))
}

func expectLeaveInsert(mock sqlmock.Sqlmock, key string, id int64) {
	mock.ExpectExec("^INSERT INTO `user_team_leave_operations` ").
		WithArgs(int64(100), int64(42), key, int64(7), int8(0)).
		WillReturnResult(sqlmock.NewResult(id, 1))
}

func TestBeginTeamLeaveIntentCommitsBothWrites(t *testing.T) {
	for _, role := range []int8{0, 1} {
		t.Run(map[int8]string{0: "member", 1: "admin"}[role], func(t *testing.T) {
			s, mock := newTestUserServer(t)
			mock.ExpectBegin()
			expectLeaveMember(mock, role, model.TeamMembershipActive, 7)
			expectLeaveTeam(mock, 99)
			expectLeaveOperation(mock, "Leave_1", emptyLeaveOperations())
			expectLeaveUpdate(mock, 7, 1)
			expectLeaveInsert(mock, "Leave_1", 89)
			mock.ExpectCommit()
			got, err := beginTeamLeaveIntent(context.Background(), s.db, 100, 42, "Leave_1")
			if err != nil || got == nil || got.ID != 89 || got.TeamID != 100 || got.UserID != 42 || got.Generation != 7 || got.Status != 0 {
				t.Fatalf("committed leave operation = %+v, %v", got, err)
			}
		})
	}
}

func TestBeginTeamLeaveIntentSameKeyReturnsExistingWithoutWrites(t *testing.T) {
	s, mock := newTestUserServer(t)
	mock.ExpectBegin()
	expectLeaveMember(mock, 0, model.TeamMembershipLeaving, 7)
	expectLeaveTeam(mock, 99)
	expectLeaveOperation(mock, "retry", emptyLeaveOperations().AddRow(89, 100, 42, 7, 0))
	mock.ExpectCommit()
	got, err := beginTeamLeaveIntent(context.Background(), s.db, 100, 42, "retry")
	if err != nil || got == nil || got.ID != 89 || got.Status != 0 || got.Generation != 7 {
		t.Fatalf("same-key retry = %+v, %v", got, err)
	}
}

func TestBeginTeamLeaveIntentCompletedRetryReturnsExisting(t *testing.T) {
	s, mock := newTestUserServer(t)
	mock.ExpectBegin()
	expectLeaveMember(mock, 1, model.TeamMembershipLeft, 7)
	expectLeaveTeam(mock, 99)
	expectLeaveOperation(mock, "done", emptyLeaveOperations().AddRow(89, 100, 42, 7, 1))
	mock.ExpectCommit()
	got, err := beginTeamLeaveIntent(context.Background(), s.db, 100, 42, "done")
	if err != nil || got == nil || got.ID != 89 || got.Status != 1 {
		t.Fatalf("completed retry = %+v, %v", got, err)
	}
}

func TestBeginTeamLeaveIntentRejectsInconsistentOldOperation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  int8
		status int8
	}{
		{"pending but active", model.TeamMembershipActive, 0},
		{"completed but leaving", model.TeamMembershipLeaving, 1},
		{"completed but active", model.TeamMembershipActive, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			mock.ExpectBegin()
			expectLeaveMember(mock, 0, tc.state, 7)
			expectLeaveTeam(mock, 99)
			expectLeaveOperation(mock, "old", emptyLeaveOperations().AddRow(89, 100, 42, 7, tc.status))
			mock.ExpectRollback()
			got, err := beginTeamLeaveIntent(context.Background(), s.db, 100, 42, "old")
			if got != nil || status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("inconsistent operation = %+v, %v", got, err)
			}
		})
	}
}

func TestBeginTeamLeaveIntentChecksTeamOwnerDespiteMemberRole(t *testing.T) {
	for _, role := range []int8{0, 1} {
		s, mock := newTestUserServer(t)
		mock.ExpectBegin()
		expectLeaveMember(mock, role, model.TeamMembershipActive, 7)
		expectLeaveTeam(mock, 42)
		mock.ExpectRollback()
		got, err := beginTeamLeaveIntent(context.Background(), s.db, 100, 42, "leave")
		if got != nil || status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("owner with member role %d = %+v, %v", role, got, err)
		}
	}
}

func TestBeginTeamLeaveIntentRejectsReusedKeyAcrossScopeOrGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, key       string
		team, user, gen int64
	}{
		{"other team", "same-key", 101, 42, 7},
		{"other user", "same-key", 100, 43, 7},
		{"old generation", "same-key", 100, 42, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			mock.ExpectBegin()
			expectLeaveMember(mock, 0, model.TeamMembershipActive, 7)
			expectLeaveTeam(mock, 99)
			expectLeaveOperation(mock, tc.key, emptyLeaveOperations().AddRow(89, tc.team, tc.user, tc.gen, 0))
			mock.ExpectRollback()
			got, err := beginTeamLeaveIntent(context.Background(), s.db, 100, 42, tc.key)
			if got != nil || status.Code(err) != codes.AlreadyExists {
				t.Fatalf("scope conflict = %+v, %v", got, err)
			}
		})
	}
}

func TestBeginTeamLeaveIntentRejectsOwnerInactiveAndCorruptMembership(t *testing.T) {
	for _, tc := range []struct {
		name        string
		role, state int8
		generation  int64
		code        codes.Code
	}{
		{"owner", 2, model.TeamMembershipActive, 7, codes.FailedPrecondition},
		{"leaving", 0, model.TeamMembershipLeaving, 7, codes.FailedPrecondition},
		{"left", 0, model.TeamMembershipLeft, 7, codes.FailedPrecondition},
		{"zero generation", 0, model.TeamMembershipActive, 0, codes.Unavailable},
		{"negative generation", 0, model.TeamMembershipActive, -1, codes.Unavailable},
		{"unknown role", 3, model.TeamMembershipActive, 7, codes.Unavailable},
		{"unknown state", 0, 3, 7, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			mock.ExpectBegin()
			expectLeaveMember(mock, tc.role, tc.state, tc.generation)
			if tc.generation > 0 && tc.role <= 2 && tc.state <= model.TeamMembershipLeft {
				owner := int64(99)
				if tc.role == 2 {
					owner = 42
				}
				expectLeaveTeam(mock, owner)
				if owner != 42 {
					expectLeaveOperation(mock, "leave", emptyLeaveOperations())
				}
			}
			mock.ExpectRollback()
			got, err := beginTeamLeaveIntent(context.Background(), s.db, 100, 42, "leave")
			if got != nil || status.Code(err) != tc.code {
				t.Fatalf("invalid membership = %+v, %v; want %v", got, err, tc.code)
			}
		})
	}
}

func TestBeginTeamLeaveIntentValidationDoesNotWrite(t *testing.T) {
	t.Run("missing database", func(t *testing.T) {
		got, err := beginTeamLeaveIntent(context.Background(), nil, 100, 42, "leave")
		if got != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("missing database = %+v, %v", got, err)
		}
	})
	for _, tc := range []struct {
		name, key  string
		team, user int64
	}{
		{"team zero", "leave", 0, 42},
		{"user zero", "leave", 100, 0},
		{"empty key", "", 100, 42},
		{"space", "not valid", 100, 42},
		{"unicode", "退出", 100, 42},
		{"overlong", strings.Repeat("x", 65), 100, 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestUserServer(t)
			got, err := beginTeamLeaveIntent(context.Background(), s.db, tc.team, tc.user, tc.key)
			if got != nil || status.Code(err) != codes.InvalidArgument {
				t.Fatalf("invalid input = %+v, %v", got, err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code codes.Code
	}{
		{"nil", nil, codes.InvalidArgument},
		{"canceled", canceledLeaveContext(), codes.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestUserServer(t)
			got, err := beginTeamLeaveIntent(tc.ctx, s.db, 100, 42, "leave")
			if got != nil || status.Code(err) != tc.code {
				t.Fatalf("invalid context = %+v, %v", got, err)
			}
		})
	}
}

func canceledLeaveContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestBeginTeamLeaveIntentReadFailuresRollback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(sqlmock.Sqlmock)
		want  codes.Code
	}{
		{"member missing", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery("^"+regexp.QuoteMeta(leaveMemberSQL)+"$").
				WithArgs(int64(100), int64(42), 1).
				WillReturnRows(sqlmock.NewRows([]string{"role", "membership_state", "generation"}))
		}, codes.PermissionDenied},
		{"member SQL failure", func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery("^"+regexp.QuoteMeta(leaveMemberSQL)+"$").
				WithArgs(int64(100), int64(42), 1).WillReturnError(errors.New("secret SQL failure"))
		}, codes.Unavailable},
		{"team missing", func(mock sqlmock.Sqlmock) {
			expectLeaveMember(mock, 0, model.TeamMembershipActive, 7)
			mock.ExpectQuery("^"+regexp.QuoteMeta(leaveTeamSQL)+"$").
				WithArgs(int64(100), 1).WillReturnRows(sqlmock.NewRows([]string{"owner_id"}))
		}, codes.Unavailable},
		{"operation SQL failure", func(mock sqlmock.Sqlmock) {
			expectLeaveMember(mock, 0, model.TeamMembershipActive, 7)
			expectLeaveTeam(mock, 99)
			mock.ExpectQuery("^"+regexp.QuoteMeta(leaveOperationSQL)+"$").
				WithArgs(int64(42), "secret-key", 1).WillReturnError(errors.New("secret SQL failure"))
		}, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			mock.ExpectBegin()
			tc.setup(mock)
			mock.ExpectRollback()
			got, err := beginTeamLeaveIntent(context.Background(), s.db, 100, 42, "secret-key")
			if got != nil || status.Code(err) != tc.want {
				t.Fatalf("read failure = %+v, %v; want %v", got, err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "secret SQL") {
				t.Fatalf("error leaks operation details: %v", err)
			}
		})
	}
}

func TestBeginTeamLeaveIntentRollsBackEachWriteFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		updateRows int64
		updateErr  error
		insertErr  error
		commitErr  error
		want       codes.Code
	}{
		{"update zero rows", 0, nil, nil, nil, codes.FailedPrecondition},
		{"update SQL failure", 0, errors.New("secret SQL failure"), nil, nil, codes.Unavailable},
		{"insert SQL failure", 1, nil, errors.New("secret SQL failure"), nil, codes.Unavailable},
		{"duplicate generation operation", 1, nil, &mysql.MySQLError{Number: 1062, Message: "secret duplicate generation"}, nil, codes.AlreadyExists},
		{"commit failure", 1, nil, nil, errors.New("secret commit failure"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			mock.ExpectBegin()
			expectLeaveMember(mock, 0, model.TeamMembershipActive, 7)
			expectLeaveTeam(mock, 99)
			expectLeaveOperation(mock, "secret-key", emptyLeaveOperations())
			update := mock.ExpectExec("^"+regexp.QuoteMeta(leaveUpdateSQL)+"$").
				WithArgs(model.TeamMembershipLeaving, int64(100), int64(42), model.TeamMembershipActive, int64(7), int8(0), int8(1))
			if tc.updateErr != nil {
				update.WillReturnError(tc.updateErr)
			} else {
				update.WillReturnResult(sqlmock.NewResult(0, tc.updateRows))
			}
			if tc.updateErr == nil && tc.updateRows == 1 {
				insert := mock.ExpectExec("^INSERT INTO `user_team_leave_operations` ").
					WithArgs(int64(100), int64(42), "secret-key", int64(7), int8(0))
				if tc.insertErr != nil {
					insert.WillReturnError(tc.insertErr)
				} else {
					insert.WillReturnResult(sqlmock.NewResult(89, 1))
				}
			}
			if tc.commitErr != nil {
				mock.ExpectCommit().WillReturnError(tc.commitErr)
			} else {
				mock.ExpectRollback()
			}
			got, err := beginTeamLeaveIntent(context.Background(), s.db, 100, 42, "secret-key")
			if got != nil || status.Code(err) != tc.want {
				t.Fatalf("failed write = %+v, %v; want %v", got, err, tc.want)
			}
			if strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "secret SQL") || strings.Contains(err.Error(), "secret commit") || strings.Contains(err.Error(), "secret duplicate") {
				t.Fatalf("error leaks operation details: %v", err)
			}
		})
	}
}

func TestBeginTeamLeaveIntentConcurrentKeyCollisionAcrossTeamsRollsBack(t *testing.T) {
	s, mock := newTestUserServer(t)
	mock.ExpectBegin()
	mock.ExpectQuery("^"+regexp.QuoteMeta(leaveMemberSQL)+"$").
		WithArgs(int64(101), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"role", "membership_state", "generation"}).AddRow(0, model.TeamMembershipActive, 7))
	mock.ExpectQuery("^"+regexp.QuoteMeta(leaveTeamSQL)+"$").
		WithArgs(int64(101), 1).
		WillReturnRows(sqlmock.NewRows([]string{"owner_id"}).AddRow(99))
	mock.ExpectQuery("^"+regexp.QuoteMeta(leaveOperationSQL)+"$").
		WithArgs(int64(42), "key-from-other-team", 1).
		WillReturnRows(emptyLeaveOperations())
	mock.ExpectExec("^"+regexp.QuoteMeta(leaveUpdateSQL)+"$").
		WithArgs(model.TeamMembershipLeaving, int64(101), int64(42), model.TeamMembershipActive, int64(7), int8(0), int8(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("^INSERT INTO `user_team_leave_operations` ").
		WithArgs(int64(101), int64(42), "key-from-other-team", int64(7), int8(0)).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "secret SQL and duplicate key"})
	mock.ExpectRollback()
	got, err := beginTeamLeaveIntent(context.Background(), s.db, 101, 42, "key-from-other-team")
	if got != nil || status.Code(err) != codes.AlreadyExists {
		t.Fatalf("cross-team concurrent key collision = %+v, %v", got, err)
	}
	if strings.Contains(err.Error(), "key-from-other-team") || strings.Contains(err.Error(), "secret SQL") {
		t.Fatalf("conflict error leaks private details: %v", err)
	}
}
