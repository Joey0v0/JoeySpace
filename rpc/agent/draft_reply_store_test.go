package agent

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func succeededReplyRun() taskDraftRun {
	run := confirmationRun()
	run.Status, run.TaskRequestKey, run.TaskID = draftSucceeded, draftTaskRequestKey(run.ID), 9007199254740993
	return run
}

func replyRows(records ...draftReplyRecord) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"run_id", "task_id", "team_id", "group_id", "initiator_id", "msg_id", "content", "accepted"})
	for _, r := range records {
		rows.AddRow(r.RunID, r.TaskID, r.TeamID, r.GroupID, r.InitiatorID, r.MsgID, r.Content, r.Accepted)
	}
	return rows
}

func TestPrepareReplyLocksSavedSuccessAndFreezesExactCard(t *testing.T) {
	store, mock := testDraftStore(t)
	run := succeededReplyRun()
	expected, _ := replyIntent(run)
	expectConfirmationLock(mock, run)
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftReply)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(replyRows())
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO agent_task_replies")).WithArgs(
		expected.RunID, expected.TaskID, expected.TeamID, expected.GroupID, expected.InitiatorID, expected.MsgID, expected.Content, false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	got, err := store.prepareReply(context.Background(), run)
	if err != nil || got != expected || !strings.Contains(got.Content, `"task_id":"9007199254740993"`) {
		t.Fatalf("prepare = %+v, %v", got, err)
	}
}

func TestPrepareReplyReusesFrozenRecordAndRejectsChanges(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(*draftReplyRecord)
		want  codes.Code
	}{
		{"pending", func(*draftReplyRecord) {}, codes.OK},
		{"accepted", func(r *draftReplyRecord) { r.Accepted = true }, codes.OK},
		{"content conflict", func(r *draftReplyRecord) { r.Content += " " }, codes.AlreadyExists},
		{"scope conflict", func(r *draftReplyRecord) { r.GroupID++ }, codes.AlreadyExists},
		{"identity conflict", func(r *draftReplyRecord) { r.InitiatorID++ }, codes.AlreadyExists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := succeededReplyRun()
			saved, _ := replyIntent(run)
			tc.alter(&saved)
			expectConfirmationLock(mock, run)
			mock.ExpectQuery(regexp.QuoteMeta(selectDraftReply)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(replyRows(saved))
			if tc.want == codes.OK {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			got, err := store.prepareReply(context.Background(), run)
			if status.Code(err) != tc.want || (err == nil && got != saved) {
				t.Fatalf("replay = %+v, %v", got, err)
			}
		})
	}
}

func TestPrepareReplyRejectsUnsavedOrChangedTaskAndMasksDBFailure(t *testing.T) {
	store, mock := testDraftStore(t)
	if _, err := store.prepareReply(context.Background(), confirmationRun()); status.Code(err) != codes.FailedPrecondition {
		t.Fatal(err)
	}
	run := succeededReplyRun()
	changed := run
	changed.TaskID++
	expectConfirmationLock(mock, changed)
	mock.ExpectRollback()
	if _, err := store.prepareReply(context.Background(), run); status.Code(err) != codes.Aborted {
		t.Fatal(err)
	}
	expectConfirmationLock(mock, run)
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftReply)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnError(errors.New("private DB detail"))
	mock.ExpectRollback()
	if _, err := store.prepareReply(context.Background(), run); status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
}

func TestLoadReplyDoesNotWriteAndChecksStoredIntent(t *testing.T) {
	store, mock := testDraftStore(t)
	run := succeededReplyRun()
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftReply)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(replyRows())
	if _, found, err := store.loadReply(context.Background(), run); found || err != nil {
		t.Fatalf("absent = %v, %v", found, err)
	}
	r, _ := replyIntent(run)
	r.Accepted = true
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftReply)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(replyRows(r))
	if got, found, err := store.loadReply(context.Background(), run); !found || err != nil || got != r {
		t.Fatalf("read = %+v, %v", got, err)
	}
	r.TaskID++
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftReply)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(replyRows(r))
	if _, _, err := store.loadReply(context.Background(), run); status.Code(err) != codes.Unavailable {
		t.Fatal(err)
	}
}

func TestAcceptReplyIsMonotonicAndVerifiesZeroChangedRows(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		store, mock := testDraftStore(t)
		r, _ := replyIntent(succeededReplyRun())
		mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_replies SET accepted = 1")).WithArgs(r.RunID, r.InitiatorID, r.MsgID).WillReturnResult(sqlmock.NewResult(0, 0))
		saved := r
		saved.Accepted = accepted
		mock.ExpectQuery(regexp.QuoteMeta(selectDraftReply)).WithArgs(r.RunID, r.InitiatorID).WillReturnRows(replyRows(saved))
		err := store.acceptReply(context.Background(), r)
		if (err == nil) != accepted {
			t.Fatalf("accepted=%v: %v", accepted, err)
		}
	}
	store, mock := testDraftStore(t)
	r, _ := replyIntent(succeededReplyRun())
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_replies SET accepted = 1")).WithArgs(r.RunID, r.InitiatorID, r.MsgID).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := store.acceptReply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestReplyMigrationMatchesFreshSchema(t *testing.T) {
	init, err := os.ReadFile("../../deploy/mysql/init.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../deploy/mysql/migrations/015_agent_task_replies.sql")
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?s)CREATE TABLE agent_task_replies \(.*?\) ENGINE=InnoDB;`)
	normalize := func(b []byte) string { return strings.Join(strings.Fields(string(pattern.Find(b))), " ") }
	if normalize(init) == "" || normalize(init) != normalize(migration) {
		t.Fatal("fresh and migrated reply schema differ")
	}
}
