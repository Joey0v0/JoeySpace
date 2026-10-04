package agent

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var triggerClaimColumns = []string{"message_id", "action", "event_version", "status", "received_at", "lease_token", "lease_until", "model_attempts", "model_started", "result_run_id", "retry_after", "retry_failures"}

type triggerClaimTokenCapture struct{ capture func(string) }

func (a triggerClaimTokenCapture) Match(value driver.Value) bool {
	token, ok := value.(string)
	if !ok || !validTriggerLeaseToken(token) {
		return false
	}
	a.capture(token)
	return true
}

func triggerClaimFixture(running bool, attempts int) triggerExecutionRow {
	row := triggerExecutionRow{Event: model.AgentTriggerEvent{MessageID: 9007199254740993, Action: model.AgentTriggerAction, Version: model.AgentTriggerVersion}, Status: TriggerInboxQueued,
		ReceivedAt: time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC), ModelAttempts: attempts}
	if running {
		row.Status = TriggerInboxRunning
		row.Token = sql.NullString{String: strings.Repeat("a", 64), Valid: true}
		row.Until = sql.NullTime{Time: time.Date(2037, 10, 4, 1, 0, 0, 123000, time.UTC), Valid: true}
		if attempts > 0 {
			row.ModelStarted = 1
		}
	}
	return row
}

func triggerClaimValues(row triggerExecutionRow) []driver.Value {
	var token, until driver.Value
	if row.Token.Valid {
		token = row.Token.String
	}
	if row.Until.Valid {
		until = row.Until.Time
	}
	return []driver.Value{row.Event.MessageID, row.Event.Action, int64(row.Event.Version), row.Status, row.ReceivedAt, token, until, int64(row.ModelAttempts), int64(row.ModelStarted), row.ResultRunID, row.RetryAfter, int64(row.RetryFailures)}
}

func triggerClaimRows(rows ...triggerExecutionRow) *sqlmock.Rows {
	result := sqlmock.NewRows(triggerClaimColumns)
	for _, row := range rows {
		result.AddRow(triggerClaimValues(row)...)
	}
	return result
}

func claimedTriggerFixture(row triggerExecutionRow) triggerExecutionRow {
	row.Status = TriggerInboxRunning
	row.Token = sql.NullString{String: strings.Repeat("b", 64), Valid: true}
	row.Until = sql.NullTime{Time: time.Date(2037, 10, 4, 2, 0, 30, 456000, time.UTC), Valid: true}
	row.ModelStarted = 0
	row.RetryAfter = sql.NullTime{}
	return row
}

func expectTriggerClaimCandidate(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectClaimableTriggerLease)).WillReturnRows(rows)
}

func expectTriggerClaimUpdate(mock sqlmock.Sqlmock, before, after triggerExecutionRow, count int64) {
	mock.ExpectExec(regexp.QuoteMeta(claimTriggerLeaseSQL)).WithArgs(after.Token.String, before.Event.MessageID, before.Status,
		int64(before.ModelAttempts), int64(before.ModelStarted), before.Token.String).WillReturnResult(sqlmock.NewResult(0, count))
}

func expectTriggerClaimLive(mock sqlmock.Sqlmock, messageID int64, token string, rows *sqlmock.Rows) {
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(messageID, token).WillReturnRows(rows)
}

func TestTriggerClaimQueuedAndExpiredRestoreTokenWithoutSpendingModelBudget(t *testing.T) {
	for _, tc := range []struct {
		running  bool
		attempts int
	}{{false, 0}, {false, 1}, {true, 0}, {true, 1}} {
		store, mock := testDraftStore(t)
		before := triggerClaimFixture(tc.running, tc.attempts)
		after := claimedTriggerFixture(before)
		expectTriggerClaimCandidate(mock, triggerClaimRows(before))
		expectTriggerClaimUpdate(mock, before, after, 1)
		expectTriggerClaimLive(mock, before.Event.MessageID, after.Token.String, triggerClaimRows(after))
		mock.ExpectCommit()
		lease, err := NewTriggerInboxStore(store.db).claimWithToken(context.Background(), func() (string, error) { return after.Token.String, nil })
		if err != nil || lease == nil || *lease != *after.lease() || lease.ModelAttempts != tc.attempts || lease.ModelStarted || tc.running && lease.Token == before.Token.String {
			t.Fatalf("claim=%+v err=%v", lease, err)
		}
	}
	if !strings.Contains(selectClaimableTriggerLease, "lease_until <= UTC_TIMESTAMP(6)") || !strings.Contains(selectClaimableTriggerLease, "FOR UPDATE SKIP LOCKED") {
		t.Fatal("claim lacks database expiry or nonblocking row locking")
	}
}

func TestTriggerClaimPublicEntryPersistsFreshCryptoTokenAndDatabaseDeadline(t *testing.T) {
	store, mock := testDraftStore(t)
	before := triggerClaimFixture(false, 0)
	after := claimedTriggerFixture(before)
	rows := sqlmock.NewRows(triggerClaimColumns)
	captured := ""
	expectTriggerClaimCandidate(mock, triggerClaimRows(before))
	mock.ExpectExec(regexp.QuoteMeta(claimTriggerLeaseSQL)).WithArgs(triggerClaimTokenCapture{capture: func(token string) {
		if captured == "" {
			captured = token
			after.Token.String = token
			rows.AddRow(triggerClaimValues(after)...)
		}
	}}, before.Event.MessageID, before.Status, int64(before.ModelAttempts), int64(before.ModelStarted), before.Token.String).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(before.Event.MessageID, sqlmock.AnyArg()).WillReturnRows(rows)
	mock.ExpectCommit()
	lease, err := NewTriggerInboxStore(store.db).Claim(context.Background())
	if err != nil || lease == nil || !validTriggerLeaseToken(captured) || *lease != *after.lease() {
		t.Fatalf("public claim=%+v %v", lease, err)
	}
}

func TestTriggerClaimEmptyQueueAndExpiredFinalAttemptReturnNoLease(t *testing.T) {
	for _, empty := range []bool{true, false} {
		store, mock := testDraftStore(t)
		row := triggerClaimFixture(true, 2)
		rows := triggerClaimRows(row)
		if empty {
			rows = triggerClaimRows()
		}
		expectTriggerClaimCandidate(mock, rows)
		if !empty {
			mock.ExpectExec(regexp.QuoteMeta(retireExpiredTriggerLeaseSQL)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectCommit()
		lease, err := NewTriggerInboxStore(store.db).claimWithToken(context.Background(), func() (string, error) { t.Fatal("empty/retired queue consumed entropy"); return "", nil })
		if err != nil || lease != nil {
			t.Fatalf("empty/retired=%+v %v", lease, err)
		}
	}
}

func TestTriggerClaimRetirementFailuresDoNotReturnLeaseOrClaimSuccess(t *testing.T) {
	for _, name := range []string{"SQL", "zero", "two", "commit"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			row := triggerClaimFixture(true, 2)
			expectTriggerClaimCandidate(mock, triggerClaimRows(row))
			exec := mock.ExpectExec(regexp.QuoteMeta(retireExpiredTriggerLeaseSQL)).WithArgs(row.Event.MessageID, row.Token.String)
			var sentinel error
			if name == "SQL" {
				exec.WillReturnError(errors.New("private SQL"))
			} else {
				count := int64(1)
				if name == "zero" {
					count = 0
					sentinel = ErrTriggerLeaseLost
				} else if name == "two" {
					count = 2
					sentinel = ErrInvalidTriggerState
				}
				exec.WillReturnResult(sqlmock.NewResult(0, count))
			}
			if name == "commit" {
				mock.ExpectCommit().WillReturnError(errors.New("private commit"))
			} else {
				mock.ExpectRollback()
			}
			lease, err := NewTriggerInboxStore(store.db).claimWithToken(context.Background(), func() (string, error) { t.Fatal("retirement generated a token"); return "", nil })
			if lease != nil || sentinel != nil && !errors.Is(err, sentinel) || sentinel == nil && status.Code(err) != codes.Unavailable {
				t.Fatalf("retirement=%+v %v", lease, err)
			}
		})
	}
}

func TestTriggerClaimRejectsDamagedCandidateWithoutMutation(t *testing.T) {
	for _, name := range []string{"exhausted", "queued has token", "queued final budget", "running missing token", "invalid token", "NULL attempts", "NULL received", "multiple"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			row := triggerClaimFixture(false, 0)
			switch name {
			case "exhausted":
				row.Status = TriggerInboxExhausted
				row.ModelAttempts = 2
			case "queued has token":
				row.Token = sql.NullString{String: strings.Repeat("a", 64), Valid: true}
			case "queued final budget":
				row.ModelAttempts = 2
			case "running missing token":
				row.Status = TriggerInboxRunning
			case "invalid token":
				row = triggerClaimFixture(true, 1)
				row.Token.String = "private-invalid-token"
			}
			rows := triggerClaimRows(row)
			if name == "multiple" {
				rows.AddRow(triggerClaimValues(row)...)
			} else if name == "NULL attempts" || name == "NULL received" {
				values := triggerClaimValues(row)
				field := 7
				if name == "NULL received" {
					field = 4
				}
				values[field] = nil
				rows = sqlmock.NewRows(triggerClaimColumns).AddRow(values...)
			}
			expectTriggerClaimCandidate(mock, rows)
			mock.ExpectRollback()
			lease, err := NewTriggerInboxStore(store.db).claimWithToken(context.Background(), func() (string, error) { t.Fatal("bad state generated token"); return "", nil })
			if lease != nil || !errors.Is(err, ErrInvalidTriggerState) {
				t.Fatalf("bad candidate=%+v %v", lease, err)
			}
		})
	}
}

func TestTriggerClaimEntropyFailureDoesNotUpdateOrExposeToken(t *testing.T) {
	for _, name := range []string{"nil generator", "entropy error", "bad token", "same old token"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			row := triggerClaimFixture(true, 1)
			expectTriggerClaimCandidate(mock, triggerClaimRows(row))
			mock.ExpectRollback()
			var token func() (string, error)
			if name != "nil generator" {
				token = func() (string, error) {
					if name == "entropy error" {
						return "", errors.New("private entropy detail")
					}
					if name == "same old token" {
						return row.Token.String, nil
					}
					return "private bad token", nil
				}
			}
			lease, err := NewTriggerInboxStore(store.db).claimWithToken(context.Background(), token)
			if lease != nil || status.Code(err) != codes.Unavailable || strings.Contains(status.Convert(err).Message(), "private") || strings.Contains(status.Convert(err).Message(), row.Token.String) {
				t.Fatalf("entropy=%+v %v", lease, err)
			}
		})
	}
	first, err := randomTriggerLeaseToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := randomTriggerLeaseToken()
	if err != nil || !validTriggerLeaseToken(first) || !validTriggerLeaseToken(second) || first == second {
		t.Fatal("crypto token generation failed")
	}
}

func TestTriggerClaimUpdateRereadAndCommitFailuresNeverReturnLease(t *testing.T) {
	for _, name := range []string{"query", "update SQL", "zero update", "two update", "missing live", "wrong live token", "budget changed", "started changed", "live SQL", "commit"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			before := triggerClaimFixture(false, 0)
			after := claimedTriggerFixture(before)
			want := codes.Unavailable
			var sentinel error
			mock.ExpectBegin()
			query := mock.ExpectQuery(regexp.QuoteMeta(selectClaimableTriggerLease))
			if name == "query" {
				query.WillReturnError(errors.New("private SQL"))
			} else {
				query.WillReturnRows(triggerClaimRows(before))
				exec := mock.ExpectExec(regexp.QuoteMeta(claimTriggerLeaseSQL))
				if name == "update SQL" {
					exec.WillReturnError(errors.New("private SQL"))
				} else {
					count := int64(1)
					if name == "zero update" {
						count = 0
						sentinel = ErrTriggerLeaseLost
					} else if name == "two update" {
						count = 2
						sentinel = ErrInvalidTriggerState
					}
					exec.WillReturnResult(sqlmock.NewResult(0, count))
					if count == 1 {
						rows := triggerClaimRows(after)
						switch name {
						case "missing live":
							rows = triggerClaimRows()
							sentinel = ErrTriggerLeaseLost
						case "wrong live token":
							after.Token.String = strings.Repeat("c", 64)
							rows = triggerClaimRows(after)
							sentinel = ErrInvalidTriggerState
						case "budget changed":
							after.ModelAttempts = 1
							rows = triggerClaimRows(after)
							sentinel = ErrInvalidTriggerState
						case "started changed":
							after.ModelStarted = 1
							rows = triggerClaimRows(after)
							sentinel = ErrInvalidTriggerState
						}
						live := mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(before.Event.MessageID, strings.Repeat("b", 64))
						if name == "live SQL" {
							live.WillReturnError(errors.New("private SQL"))
						} else {
							live.WillReturnRows(rows)
						}
					}
				}
			}
			if name == "commit" {
				mock.ExpectCommit().WillReturnError(errors.New("private commit"))
			} else {
				mock.ExpectRollback()
			}
			lease, err := NewTriggerInboxStore(store.db).claimWithToken(context.Background(), func() (string, error) { return strings.Repeat("b", 64), nil })
			if lease != nil || sentinel != nil && !errors.Is(err, sentinel) || sentinel == nil && status.Code(err) != want {
				t.Fatalf("claim failure=%+v %v", lease, err)
			}
		})
	}
}

func TestTriggerRenewUsesLiveTokenAndDatabaseTimePreservingModelState(t *testing.T) {
	for _, attempts := range []int{0, 1, 2} {
		store, mock := testDraftStore(t)
		before := triggerClaimFixture(true, attempts)
		after := before
		after.Until.Time = after.Until.Time.Add(30 * time.Second)
		input := *before.lease()
		input.Until = time.Unix(0, 0)
		input.ModelAttempts = 99
		input.ModelStarted = false
		mock.ExpectBegin()
		expectTriggerClaimLive(mock, before.Event.MessageID, before.Token.String, triggerClaimRows(before))
		mock.ExpectExec(regexp.QuoteMeta(renewTriggerLeaseSQL)).WithArgs(before.Event.MessageID, before.Token.String).WillReturnResult(sqlmock.NewResult(0, 1))
		expectTriggerClaimLive(mock, before.Event.MessageID, before.Token.String, triggerClaimRows(after))
		mock.ExpectCommit()
		lease, err := NewTriggerInboxStore(store.db).Renew(context.Background(), input)
		if err != nil || lease == nil || *lease != *after.lease() {
			t.Fatalf("renew=%+v %v", lease, err)
		}
	}
	if !strings.Contains(renewTriggerLeaseSQL, "lease_until > UTC_TIMESTAMP(6)") || !strings.Contains(renewTriggerLeaseSQL, "GREATEST(DATE_ADD(UTC_TIMESTAMP(6), INTERVAL 30 SECOND), DATE_ADD(lease_until, INTERVAL 1 MICROSECOND))") {
		t.Fatal("renew must use database time and always extend the saved deadline")
	}
}

func TestTriggerRenewRejectsExpiredOrStaleHolderAndFailedUpdates(t *testing.T) {
	for _, name := range []string{"expired", "old token", "wrong returned token", "SQL", "zero update", "two update", "missing reread", "budget changed", "started changed", "commit"} {
		t.Run(name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			row := triggerClaimFixture(true, 1)
			input := *row.lease()
			mock.ExpectBegin()
			var sentinel error
			rows := triggerClaimRows(row)
			if name == "expired" || name == "old token" {
				rows = triggerClaimRows()
				sentinel = ErrTriggerLeaseLost
			} else if name == "wrong returned token" {
				changed := row
				changed.Token.String = strings.Repeat("c", 64)
				rows = triggerClaimRows(changed)
				sentinel = ErrInvalidTriggerState
			}
			expectTriggerClaimLive(mock, row.Event.MessageID, row.Token.String, rows)
			if sentinel == nil {
				exec := mock.ExpectExec(regexp.QuoteMeta(renewTriggerLeaseSQL)).WithArgs(input.MessageID, input.Token)
				if name == "SQL" {
					exec.WillReturnError(errors.New("private SQL"))
				} else {
					count := int64(1)
					if name == "zero update" {
						count = 0
						sentinel = ErrTriggerLeaseLost
					} else if name == "two update" {
						count = 2
						sentinel = ErrInvalidTriggerState
					}
					exec.WillReturnResult(sqlmock.NewResult(0, count))
					if count == 1 {
						after := row
						after.Until.Time = after.Until.Time.Add(30 * time.Second)
						switch name {
						case "budget changed":
							after.ModelAttempts = 2
							sentinel = ErrInvalidTriggerState
						case "started changed":
							after.ModelStarted = 0
							sentinel = ErrInvalidTriggerState
						}
						rows := triggerClaimRows(after)
						if name == "missing reread" {
							rows = triggerClaimRows()
							sentinel = ErrTriggerLeaseLost
						}
						expectTriggerClaimLive(mock, row.Event.MessageID, row.Token.String, rows)
					}
				}
			}
			if name == "commit" {
				mock.ExpectCommit().WillReturnError(errors.New("private commit"))
			} else {
				mock.ExpectRollback()
			}
			lease, err := NewTriggerInboxStore(store.db).Renew(context.Background(), input)
			if lease != nil || sentinel != nil && !errors.Is(err, sentinel) || sentinel == nil && status.Code(err) != codes.Unavailable {
				t.Fatalf("renew failure=%+v %v", lease, err)
			}
		})
	}
}

func TestTriggerClaimAndRenewHonorInputCancellationAndUnavailableStorage(t *testing.T) {
	store, _ := testDraftStore(t)
	inbox := NewTriggerInboxStore(store.db)
	row := triggerClaimFixture(true, 1)
	lease := *row.lease()
	if got, err := inbox.Claim(nil); got != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil claim=%v %v", got, err)
	}
	if got, err := inbox.Renew(nil, lease); got != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("nil renew=%v %v", got, err)
	}
	for _, bad := range []TriggerLease{{}, {MessageID: -1, Token: lease.Token}, {MessageID: lease.MessageID, Token: "private invalid token"}} {
		if got, err := inbox.Renew(context.Background(), bad); got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("bad renew=%v %v", got, err)
		}
	}
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := codes.Canceled
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = codes.DeadlineExceeded
		} else {
			cancel()
		}
		if got, err := inbox.Claim(ctx); got != nil || status.Code(err) != want {
			t.Fatalf("canceled claim=%v %v", got, err)
		}
		if got, err := inbox.Renew(ctx, TriggerLease{}); got != nil || status.Code(err) != want {
			t.Fatalf("canceled renew=%v %v", got, err)
		}
		cancel()
	}
	var absent *TriggerInboxStore
	if got, err := absent.Claim(context.Background()); got != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("absent claim=%v %v", got, err)
	}
	if got, err := NewTriggerInboxStore(nil).Renew(context.Background(), lease); got != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("absent renew=%v %v", got, err)
	}
}
