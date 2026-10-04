package agent

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// These tests run the production stores and consumer with SQL substituted for
// MySQL. They check transaction order/parameters, not real DB concurrency.
const resultFlowCompleteSQL = `UPDATE agent_task_trigger_inbox
    SET status = 'completed', result_run_id = ?, lease_token = NULL, lease_until = NULL, model_started = 0
    WHERE message_id = ? AND status = 'running' AND lease_token = ? AND lease_until > UTC_TIMESTAMP(6)
    AND model_started = 1 AND model_attempts = ? AND model_attempts BETWEEN 1 AND 2 AND result_run_id IS NULL`

var resultFlowExecutionColumns = []string{"message_id", "action", "event_version", "status", "received_at", "lease_token", "lease_until", "model_attempts", "model_started", "result_run_id", "retry_after", "retry_failures"}

func resultFlowLeaseRows(row triggerExecutionRow, resultID any) *sqlmock.Rows {
	return sqlmock.NewRows(resultFlowExecutionColumns).AddRow(row.Event.MessageID, row.Event.Action, row.Event.Version, row.Status,
		row.ReceivedAt, row.Token, row.Until, row.ModelAttempts, row.ModelStarted, resultID, nil, 0)
}

func resultFlowFixture() (triggerExecutionRow, int64, draftRunScope, []taskDraft, string, string) {
	row := executionFixtureRow()
	runID := int64(math.MaxInt64)
	scope := draftRunScope{TeamID: 9007199254741001, GroupID: 9007199254741003, InitiatorID: 9007199254741005}
	reference := int64(1791079200456)
	items := []taskDraft{
		{Title: "修复缓存", Description: "保留原负责人和时间依据", AssigneeName: "张三", AssigneeID: 9007199254741007, AssigneeResolution: assigneeMatched,
			SourceMessageID: row.Event.MessageID - 1, DueAtUnixMs: 1791160200123,
			Deadline: draftDeadlineMetadata{Text: "明天15:30", Source: "message", SourceMessageID: row.Event.MessageID - 1, ReferenceUnixMs: 1791079200123,
				Timezone: draftDeadlineTimezone, Resolution: "parsed", ParsedUnixMs: 1791160200123, InstructionReferenceUnixMs: reference}},
		{Title: "整理文档", Description: "歧义仍须本人处理", AssigneeName: "李四", AssigneeResolution: assigneeAmbiguous,
			SourceMessageID: row.Event.MessageID, Deadline: draftDeadlineMetadata{Text: "明天下午", Source: "instruction", ReferenceUnixMs: reference,
				Timezone: draftDeadlineTimezone, Resolution: "needs_input", Reason: "unsupported_expression", InstructionReferenceUnixMs: reference}},
	}
	return row, runID, scope, items, triggerNotificationKey(row.Event), draftCollectionFingerprint(scope.TeamID, scope.GroupID, "整理两项任务", &reference)
}

func expectResultFlowLive(mock sqlmock.Sqlmock, row triggerExecutionRow) {
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnRows(resultFlowLeaseRows(row, nil))
}

func expectResultFlowDrafts(mock sqlmock.Sqlmock, runID int64, scope draftRunScope, items []taskDraft, key, fingerprint string) {
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionRun)).WithArgs(runID, scope.TeamID, scope.GroupID, scope.InitiatorID, key, fingerprint,
		"waiting_confirmation", "collection", int64(len(items))).WillReturnResult(sqlmock.NewResult(0, 1))
	for i, item := range items {
		mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem)).WithArgs(collectionItemArgs(runID, i, item)...).WillReturnResult(sqlmock.NewResult(0, 1))
	}
}

func expectResultFlowCompletion(mock sqlmock.Sqlmock, row triggerExecutionRow, runID int64) *sqlmock.ExpectedExec {
	return mock.ExpectExec(regexp.QuoteMeta(resultFlowCompleteSQL)).WithArgs(runID, row.Event.MessageID, row.Token.String, int64(row.ModelAttempts))
}

func TestTriggerResultFlowSavesRunItemsAndCompletedLinkInOneTransactionThenReadsOriginalEvidence(t *testing.T) {
	for _, attempts := range []int{1, 2} {
		t.Run(map[int]string{1: "first budget", 2: "final budget"}[attempts], func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row, runID, scope, items, key, fingerprint := resultFlowFixture()
			row.ModelAttempts = attempts
			mock.ExpectBegin()
			expectResultFlowLive(mock, row)
			expectResultFlowDrafts(mock, runID, scope, items, key, fingerprint)
			expectResultFlowLive(mock, row)
			expectResultFlowCompletion(mock, row, runID).WillReturnResult(sqlmock.NewResult(0, 1))
			// A nested transaction/savepoint or a live check after completed
			// would violate this sequence; there is only this one commit.
			mock.ExpectCommit()
			input := append([]taskDraft(nil), items...)
			input[0].Title, input[0].Description = "  修复缓存  ", "\n保留原负责人和时间依据\t"
			gotID, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), *row.lease(), runID, scope, input, key, fingerprint)
			if err != nil || gotID != runID || input[0].Title != "  修复缓存  " {
				t.Fatalf("atomic save or input ownership failed: %d %v", gotID, err)
			}

			rows := sqlmock.NewRows(collectionColumns)
			for i, item := range items {
				values := collectionRowValues(i, len(items), item)
				values[0], values[4], values[5], values[6] = runID, scope.TeamID, scope.GroupID, scope.InitiatorID
				rows.AddRow(values...)
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(runID, scope.InitiatorID).WillReturnRows(rows)
			collection, err := drafts.loadDraftCollectionForInitiator(context.Background(), runID, scope.InitiatorID)
			if err != nil || collection.ID != runID || collection.Scope != scope || len(collection.Items) != 2 {
				t.Fatalf("saved collection unreadable: %+v %v", collection, err)
			}
			for i, item := range collection.Items {
				if item.Draft != items[i] || item.Status != draftWaitingConfirmation || item.TaskID != 0 || item.Revision != 1 {
					t.Fatalf("item %d lost evidence or prematurely became a Task: %+v", i, item)
				}
			}

			completed := row
			completed.Status, completed.Token, completed.Until, completed.ModelStarted = TriggerInboxCompleted, sql.NullString{}, sql.NullTime{}, 0
			query := `SELECT ` + strings.Join(resultFlowExecutionColumns, ", ") + ` FROM agent_task_trigger_inbox WHERE message_id = ?`
			mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(row.Event.MessageID).WillReturnRows(resultFlowLeaseRows(completed, runID))
			stored, err := readTriggerExecutionRow(context.Background(), drafts.db, query, row.Event.MessageID)
			if err != nil || stored == nil || stored.Status != TriggerInboxCompleted || !stored.ResultRunID.Valid || stored.ResultRunID.Int64 != runID ||
				stored.ModelAttempts != attempts || stored.ModelStarted != 0 || stored.Token.Valid || stored.Until.Valid || stored.ReceivedAt != row.ReceivedAt {
				t.Fatalf("completion lost original budget/receipt or held a lease: %+v %v", stored, err)
			}
		})
	}
}

func TestTriggerResultFlowLaterItemFailureRollsBackRunAndEarlierItem(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row, runID, scope, items, key, fingerprint := resultFlowFixture()
	mock.ExpectBegin()
	expectResultFlowLive(mock, row)
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionRun)).WithArgs(runID, scope.TeamID, scope.GroupID, scope.InitiatorID, key, fingerprint,
		"waiting_confirmation", "collection", int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem)).WithArgs(collectionItemArgs(runID, 0, items[0])...).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem)).WithArgs(collectionItemArgs(runID, 1, items[1])...).WillReturnError(errors.New("private later item SQL"))
	mock.ExpectRollback()
	gotID, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), *row.lease(), runID, scope, items, key, fingerprint)
	if gotID != 0 || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), row.Token.String) {
		t.Fatalf("partial success or leaked error: %d %v", gotID, err)
	}
}

func TestTriggerResultFlowExpiredOrReplacedLeaseCannotWriteAnyDrafts(t *testing.T) {
	for _, kind := range []string{"expired", "old token"} {
		t.Run(kind, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row, runID, scope, items, key, fingerprint := resultFlowFixture()
			lease := *row.lease()
			if kind == "old token" {
				lease.Token = strings.Repeat("b", 64)
			} else {
				lease.Until = time.Unix(1, 0)
			}
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(lease.MessageID, lease.Token).WillReturnRows(sqlmock.NewRows(resultFlowExecutionColumns))
			mock.ExpectRollback()
			gotID, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), lease, runID, scope, items, key, fingerprint)
			if gotID != 0 || !errors.Is(err, ErrTriggerLeaseLost) {
				t.Fatalf("lost lease wrote a result: %d %v", gotID, err)
			}
		})
	}
}

func TestTriggerResultFlowExpiryAfterDraftInsertsRollsBackBeforeCompletion(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row, runID, scope, items, key, fingerprint := resultFlowFixture()
	mock.ExpectBegin()
	expectResultFlowLive(mock, row)
	expectResultFlowDrafts(mock, runID, scope, items, key, fingerprint)
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnRows(sqlmock.NewRows(resultFlowExecutionColumns))
	mock.ExpectRollback()
	gotID, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), *row.lease(), runID, scope, items, key, fingerprint)
	if gotID != 0 || !errors.Is(err, ErrTriggerLeaseLost) {
		t.Fatalf("expired owner committed drafts: %d %v", gotID, err)
	}
}

func TestTriggerResultFlowCompletionUpdateMustAffectExactlyOneAndCommitMustSucceed(t *testing.T) {
	for _, failure := range []string{"zero rows", "two rows", "SQL failure", "uncertain commit"} {
		t.Run(failure, func(t *testing.T) {
			drafts, mock := testDraftStore(t)
			row, runID, scope, items, key, fingerprint := resultFlowFixture()
			mock.ExpectBegin()
			expectResultFlowLive(mock, row)
			expectResultFlowDrafts(mock, runID, scope, items, key, fingerprint)
			expectResultFlowLive(mock, row)
			update := expectResultFlowCompletion(mock, row, runID)
			switch failure {
			case "zero rows":
				update.WillReturnResult(sqlmock.NewResult(0, 0))
			case "two rows":
				update.WillReturnResult(sqlmock.NewResult(0, 2))
			case "SQL failure":
				update.WillReturnError(errors.New("private complete SQL"))
			default:
				update.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			if failure == "uncertain commit" {
				mock.ExpectCommit().WillReturnError(errors.New("private commit uncertainty"))
			} else {
				mock.ExpectRollback()
			}
			gotID, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), *row.lease(), runID, scope, items, key, fingerprint)
			if gotID != 0 || err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), row.Token.String) {
				t.Fatalf("unsafe completion failure: %d %v", gotID, err)
			}
			if failure == "zero rows" && !errors.Is(err, ErrTriggerLeaseLost) || failure == "two rows" && !errors.Is(err, ErrInvalidTriggerState) ||
				(failure == "SQL failure" || failure == "uncertain commit") && status.Code(err) != codes.Unavailable {
				t.Fatalf("wrong failure boundary: %v", err)
			}
		})
	}
}

func TestTriggerResultFlowCompletedNotificationReplayAcknowledgesWithoutRecreatingAndOldOwnerCannotSave(t *testing.T) {
	drafts, mock := testDraftStore(t)
	row, runID, scope, items, key, fingerprint := resultFlowFixture()
	mock.ExpectBegin()
	expectResultFlowLive(mock, row)
	expectResultFlowDrafts(mock, runID, scope, items, key, fingerprint)
	expectResultFlowLive(mock, row)
	expectResultFlowCompletion(mock, row, runID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if gotID, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), *row.lease(), runID, scope, items, key, fingerprint); err != nil || gotID != runID {
		t.Fatalf("initial save: %d %v", gotID, err)
	}
	for replay := 0; replay < 2; replay++ {
		broker := publishedInboxFlowNotification(t)
		expectInboxFlowInsert(mock).WillReturnError(&mysql.MySQLError{Number: 1062})
		mock.ExpectQuery(regexp.QuoteMeta(selectTriggerInboxForUpdate)).WithArgs(row.Event.MessageID).WillReturnRows(sqlmock.NewRows([]string{"message_id", "action", "event_version", "status", "received_at", "result_run_id"}).
			AddRow(row.Event.MessageID, row.Event.Action, row.Event.Version, TriggerInboxCompleted, row.ReceivedAt, runID))
		mock.ExpectCommit()
		runInboxFlowConsumer(t, NewTriggerInboxStore(drafts.db), broker, mock)
		if broker.fetches != 1 || broker.commits != 1 {
			t.Fatal("completed replay was not acknowledged exactly once")
		}
	}
	// A repeated Complete call is not a new success grant, even for the old
	// key/run. The completed row no longer satisfies the live-owner query.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectLiveTriggerLease)).WithArgs(row.Event.MessageID, row.Token.String).WillReturnRows(sqlmock.NewRows(resultFlowExecutionColumns))
	mock.ExpectRollback()
	if gotID, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), *row.lease(), runID, scope, items, key, fingerprint); gotID != 0 || !errors.Is(err, ErrTriggerLeaseLost) {
		t.Fatalf("completed owner wrote again: %d %v", gotID, err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectClaimableTriggerLease)).WillReturnRows(sqlmock.NewRows(resultFlowExecutionColumns))
	mock.ExpectCommit()
	if lease, err := NewTriggerInboxStore(drafts.db).Claim(context.Background()); lease != nil || err != nil {
		t.Fatalf("completed source claimed again: %+v %v", lease, err)
	}
}

func TestTriggerResultFlowGenerationPermissionMustAlreadyBeChargedBeforeDraftWrites(t *testing.T) {
	for _, attempts := range []int{0, 1} {
		drafts, mock := testDraftStore(t)
		row, runID, scope, items, key, fingerprint := resultFlowFixture()
		row.ModelAttempts, row.ModelStarted = attempts, 0
		mock.ExpectBegin()
		expectResultFlowLive(mock, row)
		mock.ExpectRollback()
		if gotID, err := NewTriggerInboxStore(drafts.db).CompleteDraftCollection(context.Background(), *row.lease(), runID, scope, items, key, fingerprint); gotID != 0 || !errors.Is(err, ErrInvalidTriggerState) {
			t.Fatalf("uncharged generation saved: %d %v", gotID, err)
		}
	}
}
