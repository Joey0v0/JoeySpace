package agent

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const testDraftFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testDraftStore(t *testing.T) (*draftStore, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	return &draftStore{db: db}, mock
}

func TestDraftStorePersistsAndLoadsAssigneeMetadata(t *testing.T) {
	for _, state := range []draftAssigneeResolution{assigneeNone, assigneeMatched, assigneeNotFound, assigneeAmbiguous, assigneeTruncated} {
		t.Run(string(state), func(t *testing.T) {
			store, mock := testDraftStore(t)
			draft := taskDraft{Title: "整理文档", AssigneeResolution: state}
			if state != assigneeNone {
				draft.AssigneeName = "张三"
			}
			if state == assigneeMatched {
				draft.AssigneeID = 77
			}
			run := taskDraftRun{Revision: 1, ID: 9001, Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, Status: draftWaitingConfirmation, Draft: draft}
			mock.ExpectBegin()
			mock.ExpectExec("INSERT INTO agent_runs").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("INSERT INTO agent_task_drafts").WithArgs(run.ID, draft.Title, "", draft.AssigneeID, int64(0), int64(0), draft.AssigneeName, string(state), "", "", int64(0), int64(0), "", "", "", int64(0), int64(0)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			if _, err := store.saveWaitingDraft(context.Background(), run.ID, run.Scope, draft, "request-1", testDraftFingerprint); err != nil {
				t.Fatal(err)
			}
			mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(confirmationRows(run))
			loaded, err := store.loadDraftForInitiator(context.Background(), run.ID, run.Scope.InitiatorID)
			if err != nil || loaded.Draft != draft {
				t.Fatalf("stored metadata: %+v, %v", loaded, err)
			}
		})
	}
}

func TestDraftAssigneeMigrationMatchesInitializationDefaults(t *testing.T) {
	init, err := os.ReadFile("../../deploy/mysql/init.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../deploy/mysql/migrations/016_agent_draft_assignee.sql")
	if err != nil {
		t.Fatal(err)
	}
	definition := strings.SplitN(strings.SplitN(string(init), "CREATE TABLE agent_task_drafts (", 2)[1], ") ENGINE=InnoDB;", 2)[0]
	for _, column := range []string{"assignee_name VARCHAR(64) NOT NULL DEFAULT ''", "assignee_resolution VARCHAR(16) NOT NULL DEFAULT ''"} {
		normalize := func(s string) string { return strings.Join(strings.Fields(s), " ") }
		if !strings.Contains(normalize(definition), column) || !strings.Contains(normalize(string(migration)), "ADD COLUMN "+column) {
			t.Fatalf("migration default missing: %s", column)
		}
	}
}

func TestDraftStoreSavesRunAndDraftAtomically(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_runs")).
		WithArgs(int64(9001), int64(200), int64(300), int64(400), "request-1", testDraftFingerprint, "waiting_confirmation").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_task_drafts")).
		WithArgs(int64(9001), "修复缓存", "复核", int64(500), int64(1000), int64(600), "", "", "", "", int64(0), int64(0), "", "", "", int64(0), int64(0)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	id, err := store.saveWaitingDraft(context.Background(), 9001,
		draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400},
		taskDraft{Title: " 修复缓存 ", Description: " 复核 ", AssigneeID: 500, DueAtUnixMs: 1000, SourceMessageID: 600}, "request-1", testDraftFingerprint)
	if err != nil || id != 9001 {
		t.Fatalf("save = %d, %v", id, err)
	}
}

func TestDraftStoreRollsBackWhenDraftInsertFails(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_runs")).
		WithArgs(int64(9001), int64(200), int64(300), int64(400), "request-1", testDraftFingerprint, "waiting_confirmation").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_task_drafts")).
		WillReturnError(errors.New("private database detail"))
	mock.ExpectRollback()
	_, err := store.saveWaitingDraft(context.Background(), 9001,
		draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, taskDraft{Title: "修复缓存"}, "request-1", testDraftFingerprint)
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private database detail") {
		t.Fatalf("save error = %v", err)
	}
}

func TestDraftStoreRejectsDuplicateRunID(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_runs")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate run ID"})
	mock.ExpectRollback()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, team_id, group_id, request_fingerprint FROM agent_runs")).
		WithArgs(int64(400), "request-1").WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"}))
	_, err := store.saveWaitingDraft(context.Background(), 9001,
		draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, taskDraft{Title: "修复缓存"}, "request-1", testDraftFingerprint)
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate save error = %v", err)
	}
}

func TestDraftStoreReplaysSameInitiatorAndRequestKey(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_runs")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate request"})
	mock.ExpectRollback()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, team_id, group_id, request_fingerprint FROM agent_runs")).
		WithArgs(int64(400), "request-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"}).
			AddRow(int64(8123), int64(200), int64(300), testDraftFingerprint))
	id, err := store.saveWaitingDraft(context.Background(), 9001,
		draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, taskDraft{Title: "修复缓存"}, "request-1", testDraftFingerprint)
	if err != nil || id != 8123 {
		t.Fatalf("replay = %d, %v", id, err)
	}
}

func TestDraftStoreRejectsKeyReusedForDifferentRequest(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_runs")).
		WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate request"})
	mock.ExpectRollback()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, team_id, group_id, request_fingerprint FROM agent_runs")).
		WithArgs(int64(400), "request-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"}).
			AddRow(int64(8123), int64(200), int64(300), strings.Repeat("b", 64)))
	id, err := store.saveWaitingDraft(context.Background(), 9001,
		draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, taskDraft{Title: "修复缓存"}, "request-1", testDraftFingerprint)
	if id != 0 || status.Code(err) != codes.AlreadyExists {
		t.Fatalf("conflicting replay = %d, %v", id, err)
	}
}

func TestDraftStoreRejectsInvalidRequestIdentityBeforeWriting(t *testing.T) {
	store, _ := testDraftStore(t)
	for _, tc := range []struct{ key, fingerprint string }{
		{"bad key", testDraftFingerprint},
		{"request-1", "short"},
	} {
		id, err := store.saveWaitingDraft(context.Background(), 9001,
			draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, taskDraft{Title: "修复缓存"}, tc.key, tc.fingerprint)
		if id != 0 || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid identity = %d, %v", id, err)
		}
	}
}

func TestDraftStoreLooksUpExistingRequestAndMasksReadFailure(t *testing.T) {
	store, mock := testDraftStore(t)
	scope := draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}
	query := regexp.QuoteMeta("SELECT id, team_id, group_id, request_fingerprint FROM agent_runs")
	mock.ExpectQuery(query).WithArgs(int64(400), "request-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "group_id", "request_fingerprint"}))
	id, err := store.findExistingDraft(context.Background(), scope, "request-1", testDraftFingerprint)
	if err != nil || id != 0 {
		t.Fatalf("new request lookup = %d, %v", id, err)
	}
	mock.ExpectQuery(query).WithArgs(int64(400), "request-1").
		WillReturnError(errors.New("private database detail"))
	id, err = store.findExistingDraft(context.Background(), scope, "request-1", testDraftFingerprint)
	if id != 0 || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private database detail") {
		t.Fatalf("failed request lookup = %d, %v", id, err)
	}
}

func TestDraftStoreLoadsOnlyInitiatorsDraft(t *testing.T) {
	store, mock := testDraftStore(t)
	columns := []string{"team_id", "group_id", "initiator_id", "status", "title", "description", "assignee_id", "due_at_unix_ms", "source_message_id", "revision"}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT r.team_id")).WithArgs(int64(9001), int64(400)).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(200, 300, 400, "waiting_confirmation", "修复缓存", "复核", 500, 1000, 600, 1))
	run, err := store.loadDraftForInitiator(context.Background(), 9001, 400)
	if err != nil || run.ID != 9001 || run.Scope.TeamID != 200 || run.Scope.GroupID != 300 ||
		run.Scope.InitiatorID != 400 || run.Status != draftWaitingConfirmation ||
		run.Draft.Title != "修复缓存" || run.Draft.Description != "复核" ||
		run.Draft.AssigneeID != 500 || run.Draft.DueAtUnixMs != 1000 || run.Draft.SourceMessageID != 600 {
		t.Fatalf("loaded run = %+v, %v", run, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT r.team_id")).WithArgs(int64(9001), int64(401)).
		WillReturnRows(sqlmock.NewRows(columns))
	other, err := store.loadDraftForInitiator(context.Background(), 9001, 401)
	if status.Code(err) != codes.NotFound || other.ID != 0 {
		t.Fatalf("other actor loaded run = %+v, %v", other, err)
	}
}

func TestDraftStoreMasksReadFailureAndRejectsInvalidIDs(t *testing.T) {
	store, mock := testDraftStore(t)
	if _, err := store.loadDraftForInitiator(context.Background(), 9001, 0); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid actor error = %v", err)
	}
	if _, err := store.saveWaitingDraft(context.Background(), 0, draftRunScope{}, taskDraft{}, "request-1", testDraftFingerprint); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid run error = %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT r.team_id")).WithArgs(int64(9001), int64(400)).
		WillReturnError(errors.New("private database detail"))
	_, err := store.loadDraftForInitiator(context.Background(), 9001, 400)
	if status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private database detail") {
		t.Fatalf("read error = %v", err)
	}
}
