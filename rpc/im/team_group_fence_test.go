package main

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const fenceTestInitSQL = `INSERT INTO im_team_group_fences (team_id, user_id, closed_through_generation) VALUES (?, ?, 0) ON DUPLICATE KEY UPDATE team_id = team_id`
const fenceTestLockSQL = `SELECT team_id, user_id, closed_through_generation FROM im_team_group_fences WHERE team_id = ? AND user_id = ? FOR UPDATE`
const fenceTestAdvanceSQL = `UPDATE im_team_group_fences SET closed_through_generation = ? WHERE team_id = ? AND user_id = ? AND closed_through_generation = ?`
const fenceTestDeleteSQL = "DELETE gm FROM group_members AS gm JOIN `groups` AS g ON g.id = gm.group_id WHERE g.team_id = ? AND gm.user_id = ?"

func fenceTestRows(closed int64) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"team_id", "user_id", "closed_through_generation"}).AddRow(int64(200), int64(42), closed)
}

func fenceTestLock(mock sqlmock.Sqlmock, closed int64) {
	mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestInitSQL)+"$").WithArgs(int64(200), int64(42)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("^"+regexp.QuoteMeta(fenceTestLockSQL)+"$").WithArgs(int64(200), int64(42)).WillReturnRows(fenceTestRows(closed))
}

func fenceTestClose(mock sqlmock.Sqlmock, before, after, deleted int64) {
	mock.ExpectBegin()
	fenceTestLock(mock, before)
	mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestAdvanceSQL)+"$").WithArgs(after, int64(200), int64(42), before).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestDeleteSQL)+"$").WithArgs(int64(200), int64(42)).
		WillReturnResult(sqlmock.NewResult(0, deleted))
	mock.ExpectCommit()
}

func TestTeamGroupFenceCloseAdvancesOnlyScopedMembershipsAndReplayDoesNotDelete(t *testing.T) {
	s, mock := testIMServer(t)
	// The exact joined DELETE excludes other teams/users and NULL-team old groups.
	// No deletion of messages, offline deliveries or reading receipts is expected.
	fenceTestClose(mock, 0, 3, 2)
	closed, err := closeTeamGroupMemberships(context.Background(), s.db, 200, 42, 3)
	if err != nil || closed != 3 {
		t.Fatalf("initial closure = %d, error = %v", closed, err)
	}
	for _, generation := range []int64{3, 1} {
		mock.ExpectBegin()
		fenceTestLock(mock, 3)
		mock.ExpectCommit()
		closed, err = closeTeamGroupMemberships(context.Background(), s.db, 200, 42, generation)
		if err != nil || closed != 3 {
			t.Fatalf("replay generation %d returned %d, error = %v", generation, closed, err)
		}
	}
	fenceTestClose(mock, 3, math.MaxInt64, 0)
	closed, err = closeTeamGroupMemberships(context.Background(), s.db, 200, 42, math.MaxInt64)
	if err != nil || closed != math.MaxInt64 {
		t.Fatalf("maximum closure = %d, error = %v", closed, err)
	}
}

func TestTeamGroupFenceAllowsNewWriteAndOldCleanupCannotTouchIt(t *testing.T) {
	s, mock := testIMServer(t)
	fenceTestClose(mock, 0, 3, 2)
	if _, err := closeTeamGroupMemberships(context.Background(), s.db, 200, 42, 3); err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	fenceTestLock(mock, 3)
	writeSQL := "INSERT INTO group_members (group_id, user_id, role) VALUES (?, ?, ?)"
	mock.ExpectExec("^"+regexp.QuoteMeta(writeSQL)+"$").WithArgs(int64(300), int64(42), 0).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	called := 0
	err := withTeamGroupGeneration(context.Background(), s.db, 200, 42, 4, func(tx *gorm.DB) error {
		called++
		if tx == s.db {
			t.Error("write callback did not receive the local transaction")
		}
		return tx.Exec(writeSQL, int64(300), int64(42), 0).Error
	})
	if err != nil || called != 1 {
		t.Fatalf("new generation write: callback = %d, error = %v", called, err)
	}
	mock.ExpectBegin()
	fenceTestLock(mock, 3)
	mock.ExpectCommit()
	closed, err := closeTeamGroupMemberships(context.Background(), s.db, 200, 42, 2)
	if err != nil || closed != 3 {
		t.Fatalf("old cleanup after new write = %d, error = %v", closed, err)
	}
	for _, generation := range []int64{3, 1} {
		mock.ExpectBegin()
		fenceTestLock(mock, 3)
		mock.ExpectRollback()
		err = withTeamGroupGeneration(context.Background(), s.db, 200, 42, generation, func(*gorm.DB) error { called++; return nil })
		if status.Code(err) != codes.PermissionDenied || called != 1 {
			t.Fatalf("closed generation %d: callback = %d, error = %v", generation, called, err)
		}
	}
}

func TestTeamGroupFenceRejectsInvalidInputsBeforeTransactions(t *testing.T) {
	s, _ := testIMServer(t)
	for _, tuple := range [][3]int64{{0, 42, 1}, {200, -1, 1}, {200, 42, 0}, {200, 42, -1}} {
		closed, err := closeTeamGroupMemberships(context.Background(), s.db, tuple[0], tuple[1], tuple[2])
		if closed != 0 || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid closure = %d, error = %v", closed, err)
		}
		err = withTeamGroupGeneration(context.Background(), s.db, tuple[0], tuple[1], tuple[2], func(*gorm.DB) error { t.Error("invalid write invoked callback"); return nil })
		if status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if _, err := closeTeamGroupMemberships(nil, s.db, 200, 42, 1); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if err := withTeamGroupGeneration(nil, s.db, 200, 42, 1, func(*gorm.DB) error { return nil }); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
	if _, err := closeTeamGroupMemberships(context.Background(), nil, 200, 42, 1); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	if err := withTeamGroupGeneration(context.Background(), nil, 200, 42, 1, func(*gorm.DB) error { return nil }); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
	if err := withTeamGroupGeneration(context.Background(), s.db, 200, 42, 1, nil); status.Code(err) != codes.InvalidArgument {
		t.Fatal(err)
	}
}

func TestTeamGroupFenceCloseTransactionFailuresNeverReturnClosure(t *testing.T) {
	for _, stage := range []string{"begin", "initialize", "lock", "advance", "advance missing", "delete", "commit"} {
		t.Run(stage, func(t *testing.T) {
			s, mock := testIMServer(t)
			privateErr := errors.New("private token password SQL detail")
			if stage == "begin" {
				mock.ExpectBegin().WillReturnError(privateErr)
			} else {
				mock.ExpectBegin()
				init := mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestInitSQL)+"$").WithArgs(int64(200), int64(42))
				if stage == "initialize" {
					init.WillReturnError(privateErr)
				} else {
					init.WillReturnResult(sqlmock.NewResult(0, 0))
					lock := mock.ExpectQuery("^"+regexp.QuoteMeta(fenceTestLockSQL)+"$").WithArgs(int64(200), int64(42))
					if stage == "lock" {
						lock.WillReturnError(privateErr)
					} else {
						lock.WillReturnRows(fenceTestRows(2))
						advance := mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestAdvanceSQL)+"$").WithArgs(int64(3), int64(200), int64(42), int64(2))
						if stage == "advance" {
							advance.WillReturnError(privateErr)
						} else if stage == "advance missing" {
							advance.WillReturnResult(sqlmock.NewResult(0, 0))
						} else {
							advance.WillReturnResult(sqlmock.NewResult(0, 1))
							deletion := mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestDeleteSQL)+"$").WithArgs(int64(200), int64(42))
							if stage == "delete" {
								deletion.WillReturnError(privateErr)
							} else {
								deletion.WillReturnResult(sqlmock.NewResult(0, 2))
							}
						}
					}
				}
				if stage == "commit" {
					mock.ExpectCommit().WillReturnError(privateErr)
				} else {
					mock.ExpectRollback()
				}
			}
			closed, err := closeTeamGroupMemberships(context.Background(), s.db, 200, 42, 3)
			if closed != 0 || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
				t.Fatalf("stage %s closure = %d, error = %v", stage, closed, err)
			}
		})
	}
}

func TestTeamGroupFenceInvalidLockedRowsFailClosed(t *testing.T) {
	for _, rowKind := range []string{"empty", "duplicate", "wrong team", "wrong user", "negative", "null", "row error"} {
		t.Run(rowKind, func(t *testing.T) {
			s, mock := testIMServer(t)
			rows := sqlmock.NewRows([]string{"team_id", "user_id", "closed_through_generation"})
			switch rowKind {
			case "duplicate":
				rows.AddRow(200, 42, 1).AddRow(200, 42, 1)
			case "wrong team":
				rows.AddRow(201, 42, 1)
			case "wrong user":
				rows.AddRow(200, 43, 1)
			case "negative":
				rows.AddRow(200, 42, -1)
			case "null":
				rows.AddRow(200, 42, nil)
			case "row error":
				rows.AddRow(200, 42, 1).RowError(0, errors.New("private scan detail"))
			}
			mock.ExpectBegin()
			mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestInitSQL)+"$").WithArgs(int64(200), int64(42)).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery("^"+regexp.QuoteMeta(fenceTestLockSQL)+"$").WithArgs(int64(200), int64(42)).WillReturnRows(rows)
			mock.ExpectRollback()
			called := false
			err := withTeamGroupGeneration(context.Background(), s.db, 200, 42, 3, func(*gorm.DB) error { called = true; return nil })
			if called || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
				t.Fatalf("invalid row %s invoked callback = %t, error = %v", rowKind, called, err)
			}
		})
	}
}

func TestTeamGroupFenceWriteFailureRollsBackAndHidesCallbackText(t *testing.T) {
	for _, callbackErr := range []error{errors.New("private callback SQL password"), status.Error(codes.NotFound, "private missing group detail")} {
		s, mock := testIMServer(t)
		mock.ExpectBegin()
		fenceTestLock(mock, 1)
		mock.ExpectRollback()
		err := withTeamGroupGeneration(context.Background(), s.db, 200, 42, 2, func(*gorm.DB) error { return callbackErr })
		want := status.Code(callbackErr)
		if want == codes.Unknown {
			want = codes.Unavailable
		}
		if status.Code(err) != want || strings.Contains(err.Error(), "private") {
			t.Fatalf("callback failure = %v", err)
		}
	}
	s, mock := testIMServer(t)
	mock.ExpectBegin()
	fenceTestLock(mock, 1)
	mock.ExpectCommit().WillReturnError(errors.New("private commit detail"))
	err := withTeamGroupGeneration(context.Background(), s.db, 200, 42, 2, func(*gorm.DB) error { return nil })
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
}

func TestTeamGroupFenceCancellationIsPreservedAndCannotCommitCallback(t *testing.T) {
	s, _ := testIMServer(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, expire := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expire()
	for _, ctx := range []context.Context{canceled, expired} {
		want := status.Code(status.FromContextError(ctx.Err()).Err())
		closed, err := closeTeamGroupMemberships(ctx, s.db, 200, 42, 1)
		if closed != 0 || status.Code(err) != want {
			t.Fatalf("canceled close = %d, error = %v", closed, err)
		}
		err = withTeamGroupGeneration(ctx, s.db, 200, 42, 1, func(*gorm.DB) error { t.Error("canceled write invoked callback"); return nil })
		if status.Code(err) != want {
			t.Fatal(err)
		}
	}
	s, mock := testIMServer(t)
	mock.ExpectBegin()
	fenceTestLock(mock, 0)
	mock.ExpectRollback()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	err := withTeamGroupGeneration(ctx, s.db, 200, 42, 1, func(*gorm.DB) error { stop(); return nil })
	if status.Code(err) != codes.Canceled {
		t.Fatalf("canceled callback = %v", err)
	}
	// database/sql can roll back its canceled transaction asynchronously.
	until := time.Now().Add(time.Second)
	for {
		if err := mock.ExpectationsWereMet(); err == nil {
			break
		} else if time.Now().After(until) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTeamGroupFenceCloseDeadlineDuringLockRollsBack(t *testing.T) {
	s, mock := testIMServer(t)
	mock.ExpectBegin()
	mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestInitSQL)+"$").WithArgs(int64(200), int64(42)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("^"+regexp.QuoteMeta(fenceTestLockSQL)+"$").WithArgs(int64(200), int64(42)).
		WillDelayFor(time.Second).WillReturnRows(fenceTestRows(0))
	mock.ExpectRollback()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	closed, err := closeTeamGroupMemberships(ctx, s.db, 200, 42, 1)
	if closed != 0 || status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("lock deadline closure = %d, error = %v", closed, err)
	}
	until := time.Now().Add(time.Second)
	for {
		if err := mock.ExpectationsWereMet(); err == nil {
			break
		} else if time.Now().After(until) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}
