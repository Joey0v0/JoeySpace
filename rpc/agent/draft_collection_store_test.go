package agent

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var collectionColumns = []string{"run_id", "draft_mode", "item_count", "run_status", "team_id", "group_id", "initiator_id", "item_index", "status", "title", "description", "assignee_id", "due_at_unix_ms", "source_message_id", "task_request_key", "task_id", "assignee_name", "assignee_resolution", "revision", "deadline_text", "deadline_source", "deadline_source_message_id", "deadline_reference_unix_ms", "deadline_timezone", "deadline_resolution", "deadline_reason", "deadline_parsed_unix_ms", "instruction_reference_unix_ms"}

func collectionStoreDraft() taskDraft {
	return taskDraft{Title: "修复缓存", AssigneeResolution: assigneeNone, Deadline: draftDeadlineMetadata{Source: "none", Timezone: draftDeadlineTimezone, Resolution: "none"}}
}

func collectionItemArgs(runID int64, index int, draft taskDraft) []driver.Value {
	d := draft.Deadline
	return []driver.Value{runID, int64(index), draft.Title, draft.Description, draft.AssigneeID, draft.DueAtUnixMs, draft.SourceMessageID, draft.AssigneeName, string(draft.AssigneeResolution), d.Text, d.Source, d.SourceMessageID, d.ReferenceUnixMs, d.Timezone, d.Resolution, d.Reason, d.ParsedUnixMs, d.InstructionReferenceUnixMs, "waiting_confirmation"}
}

func collectionRowValues(index, count int, draft taskDraft) []driver.Value {
	d := draft.Deadline
	return []driver.Value{int64(9001), "collection", int64(count), "waiting_confirmation", int64(200), int64(300), int64(400), int64(index), "waiting_confirmation", draft.Title, draft.Description, draft.AssigneeID, draft.DueAtUnixMs, draft.SourceMessageID, "", int64(0), draft.AssigneeName, string(draft.AssigneeResolution), int64(1), d.Text, d.Source, d.SourceMessageID, d.ReferenceUnixMs, d.Timezone, d.Resolution, d.Reason, d.ParsedUnixMs, d.InstructionReferenceUnixMs}
}

func collectionRows(drafts ...taskDraft) *sqlmock.Rows {
	rows := sqlmock.NewRows(collectionColumns)
	for i, draft := range drafts {
		rows.AddRow(collectionRowValues(i, len(drafts), draft)...)
	}
	return rows
}

func TestDraftCollectionStoreAtomicSaveNormalizesAndLoadsAllItems(t *testing.T) {
	store, mock := testDraftStore(t)
	draft := collectionStoreDraft()
	second := draft
	second.Title = "整理文档"
	scope := draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionRun)).WithArgs(int64(9001), int64(200), int64(300), int64(400), "batch-1", testDraftFingerprint, "waiting_confirmation", "collection", int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem)).WithArgs(collectionItemArgs(9001, 0, draft)...).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem)).WithArgs(collectionItemArgs(9001, 1, second)...).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	untrimmed := draft
	untrimmed.Title = " 修复缓存 "
	id, err := store.saveWaitingDraftCollection(context.Background(), 9001, scope, []taskDraft{untrimmed, second}, "batch-1", testDraftFingerprint)
	if err != nil || id != 9001 {
		t.Fatalf("save=%d, %v", id, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(collectionRows(draft, second))
	collection, err := store.loadDraftCollectionForInitiator(context.Background(), 9001, 400)
	if err != nil || collection.ID != 9001 || collection.Scope != scope || len(collection.Items) != 2 || collection.Items[0].Draft != draft || collection.Items[1].Draft != second {
		t.Fatalf("load=%+v, %v", collection, err)
	}
}

func TestDraftCollectionStoreSecondInsertFailureRollsBackEverything(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionRun)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionItem)).WillReturnError(errors.New("private database detail"))
	mock.ExpectRollback()
	id, err := store.saveWaitingDraftCollection(context.Background(), 9001, draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, []taskDraft{collectionStoreDraft(), collectionStoreDraft()}, "batch-1", testDraftFingerprint)
	if id != 0 || status.Code(err) != codes.Unavailable {
		t.Fatalf("rollback=%d, %v", id, err)
	}
}

func TestDraftCollectionStoreDuplicateRechecksWinnerAndFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name        string
		winnerID    int64
		fingerprint string
		code        codes.Code
	}{
		{"same request winner", 8123, testDraftFingerprint, codes.OK},
		{"other request", 8123, draftPreparationFingerprint(200, 300, "提取待办"), codes.AlreadyExists},
		{"unrelated duplicate", 0, "", codes.AlreadyExists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta(insertDraftCollectionRun)).WillReturnError(&mysql.MySQLError{Number: 1062})
			mock.ExpectRollback()
			rows := sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"})
			if tc.winnerID > 0 {
				rows.AddRow(tc.winnerID, int64(200), int64(300), tc.fingerprint)
			}
			mock.ExpectQuery("SELECT id, team_id, group_id, request_fingerprint FROM agent_runs").WithArgs(int64(400), "batch-1").WillReturnRows(rows)
			id, err := store.saveWaitingDraftCollection(context.Background(), 9001, draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, []taskDraft{collectionStoreDraft()}, "batch-1", testDraftFingerprint)
			if status.Code(err) != tc.code || (tc.code == codes.OK && id != tc.winnerID) || (tc.code != codes.OK && id != 0) {
				t.Fatalf("duplicate=%d, %v", id, err)
			}
		})
	}
}

func TestDraftCollectionReadRejectsDamagedOrWrongModeRecords(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]driver.Value)
		count  int
		code   codes.Code
	}{
		{"single mode", func(r []driver.Value) { r[1] = "single" }, 1, codes.FailedPrecondition},
		{"unknown mode", func(r []driver.Value) { r[1] = "unknown" }, 1, codes.Unavailable},
		{"missing item", func(r []driver.Value) { r[7] = nil }, 1, codes.Unavailable},
		{"left join no draft", func(r []driver.Value) {
			for i := 7; i < len(r); i++ {
				r[i] = nil
			}
		}, 1, codes.Unavailable},
		{"zero count", func(r []driver.Value) { r[2] = int64(0) }, 1, codes.Unavailable},
		{"missing second", func(r []driver.Value) { r[2] = int64(2) }, 1, codes.Unavailable},
		{"duplicate index", func(r []driver.Value) { r[7] = int64(0) }, 2, codes.Unavailable},
		{"negative index", func(r []driver.Value) { r[7] = int64(-1) }, 1, codes.Unavailable},
		{"noncontinuous index", func(r []driver.Value) { r[7] = int64(2) }, 2, codes.Unavailable},
		{"oversized", func(r []driver.Value) { r[2] = int64(6) }, 6, codes.Unavailable},
		{"wrong scope", func(r []driver.Value) { r[5] = int64(301) }, 2, codes.Unavailable},
		{"wrong run", func(r []driver.Value) { r[0] = int64(9002) }, 1, codes.Unavailable},
		{"wrong actor", func(r []driver.Value) { r[6] = int64(401) }, 1, codes.Unavailable},
		{"wrong run status", func(r []driver.Value) { r[3] = "succeeded" }, 1, codes.Unavailable},
		{"wrong item status", func(r []driver.Value) { r[8] = "creating" }, 1, codes.Unavailable},
		{"frozen key", func(r []driver.Value) { r[14] = "agent-task-9001-0" }, 1, codes.Unavailable},
		{"task result", func(r []driver.Value) { r[15] = int64(500) }, 1, codes.Unavailable},
		{"missing revision", func(r []driver.Value) { r[18] = int64(0) }, 1, codes.Unavailable},
		{"legacy assignee", func(r []driver.Value) { r[17] = "" }, 1, codes.Unavailable},
		{"legacy deadline", func(r []driver.Value) { r[20] = ""; r[23] = ""; r[24] = "" }, 1, codes.Unavailable},
		{"unnormalized title", func(r []driver.Value) { r[9] = " 修复缓存 " }, 1, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			rows := sqlmock.NewRows(collectionColumns)
			for i := 0; i < tc.count; i++ {
				row := collectionRowValues(i, tc.count, collectionStoreDraft())
				if i == tc.count-1 {
					tc.mutate(row)
				}
				rows.AddRow(row...)
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(rows)
			collection, err := store.loadDraftCollectionForInitiator(context.Background(), 9001, 400)
			if collection.ID != 0 || collection.Items != nil || status.Code(err) != tc.code {
				t.Fatalf("damaged=%+v, %v", collection, err)
			}
		})
	}
}

func TestDraftCollectionStoreMissingRunIsNotFound(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftCollectionForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(sqlmock.NewRows(collectionColumns))
	collection, err := store.loadDraftCollectionForInitiator(context.Background(), 9001, 400)
	if collection.ID != 0 || collection.Items != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("missing run=%+v, %v", collection, err)
	}
}

func TestLegacyStoreReadAndFreezeRejectCollectionMarker(t *testing.T) {
	store, mock := testDraftStore(t)
	run := taskDraftRun{ID: 9001, Revision: 1, Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, Status: "collection", Draft: collectionStoreDraft()}
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator)).WithArgs(int64(9001), int64(400)).WillReturnRows(confirmationRows(run))
	if loaded, err := store.loadDraftForInitiator(context.Background(), 9001, 400); loaded.ID != 0 || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy read=%+v, %v", loaded, err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator+" FOR UPDATE")).WithArgs(int64(9001), int64(400)).WillReturnRows(confirmationRows(run))
	mock.ExpectRollback()
	if loaded, err := store.freezeDraft(context.Background(), 9001, 400, run.Draft.Title, "", 1, nil, nil, "none"); loaded.ID != 0 || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("legacy freeze=%+v, %v", loaded, err)
	}
}
