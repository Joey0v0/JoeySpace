package agent

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func confirmationRows(run taskDraftRun) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"team_id", "group_id", "initiator_id", "status", "title", "description",
		"assignee_id", "due_at_unix_ms", "source_message_id", "task_request_key", "task_id", "assignee_name", "assignee_resolution", "revision"}).
		AddRow(run.Scope.TeamID, run.Scope.GroupID, run.Scope.InitiatorID, string(run.Status),
			run.Draft.Title, run.Draft.Description, run.Draft.AssigneeID, run.Draft.DueAtUnixMs,
			run.Draft.SourceMessageID, run.TaskRequestKey, run.TaskID, run.Draft.AssigneeName, string(run.Draft.AssigneeResolution), run.Revision)
}

func confirmationRun() taskDraftRun {
	return taskDraftRun{Revision: 1, ID: 9001, Scope: draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400},
		Status: draftWaitingConfirmation,
		Draft:  taskDraft{Title: "修复缓存", Description: "复核", AssigneeID: 500, DueAtUnixMs: 1000, SourceMessageID: 600}}
}

func expectConfirmationLock(mock sqlmock.Sqlmock, run taskDraftRun) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator+" FOR UPDATE")).
		WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(confirmationRows(run))
}

func TestFreezeDraftReservesKeyAndStateAtomically(t *testing.T) {
	store, mock := testDraftStore(t)
	run := confirmationRun()
	expectConfirmationLock(mock, run)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_request_key = ?")).
		WithArgs("agent-task-9001-0", run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ? WHERE id = ? AND initiator_id = ?")).
		WithArgs("creating", run.ID, run.Scope.InitiatorID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	frozen, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, 1)
	if err != nil || frozen.Status != draftCreating || frozen.TaskRequestKey != "agent-task-9001-0" ||
		frozen.Draft != run.Draft || frozen.TaskID != 0 {
		t.Fatalf("freeze = %+v, %v", frozen, err)
	}
}

func TestFreezeDraftRejectsNamedDraftBeforeAnyStateChange(t *testing.T) {
	for _, state := range []draftAssigneeResolution{assigneeMatched, assigneeNotFound, assigneeAmbiguous, assigneeTruncated} {
		store, mock := testDraftStore(t)
		run := confirmationRun()
		run.Draft.AssigneeName = "张三"
		run.Draft.AssigneeResolution = state
		if state != assigneeMatched {
			run.Draft.AssigneeID = 0
		}
		expectConfirmationLock(mock, run)
		mock.ExpectRollback()
		if _, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, 1); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("old contract: %s, %v", state, err)
		}
	}
}

func TestFreezeDraftRejectsStaleTextAndInvalidPersistedState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskDraftRun)
		code   codes.Code
	}{
		{"concurrent edit", func(r *taskDraftRun) { r.Draft.Title = "其他已保存标题" }, codes.Aborted},
		{"unknown state", func(r *taskDraftRun) { r.Status = "cancelled" }, codes.FailedPrecondition},
		{"wrong frozen key", func(r *taskDraftRun) { r.Status = draftCreating; r.TaskRequestKey = "other" }, codes.Unavailable},
		{"success missing ID", func(r *taskDraftRun) { r.Status = draftSucceeded; r.TaskRequestKey = "agent-task-9001-0" }, codes.Unavailable},
		{"waiting with prior result", func(r *taskDraftRun) { r.TaskID = 123 }, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			tc.mutate(&run)
			expectConfirmationLock(mock, run)
			mock.ExpectRollback()
			got, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, "修复缓存", "复核", 1)
			if got.ID != 0 || status.Code(err) != tc.code {
				t.Fatalf("freeze = %+v, %v", got, err)
			}
		})
	}
}

func TestFreezeDraftReplaysPersistedCreatingAndSuccessWithoutChangingPayload(t *testing.T) {
	for _, state := range []draftRunStatus{draftCreating, draftSucceeded} {
		t.Run(string(state), func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			run.Status, run.TaskRequestKey = state, "agent-task-9001-0"
			if state == draftSucceeded {
				run.TaskID = 888
			}
			expectConfirmationLock(mock, run)
			mock.ExpectCommit()
			got, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, 1)
			if err != nil || got != run {
				t.Fatalf("replay = %+v, %v", got, err)
			}
		})
	}
}

func TestFreezeDraftRollsBackKeyWhenStateWriteFails(t *testing.T) {
	store, mock := testDraftStore(t)
	run := confirmationRun()
	expectConfirmationLock(mock, run)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_request_key = ?")).
		WithArgs("agent-task-9001-0", run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).
		WillReturnError(errors.New("private database detail"))
	mock.ExpectRollback()
	got, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, 1)
	if got.ID != 0 || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private database detail") {
		t.Fatalf("freeze failure = %+v, %v", got, err)
	}
}

func TestCompleteDraftStoresTaskIDAndSuccessAtomically(t *testing.T) {
	store, mock := testDraftStore(t)
	run := confirmationRun()
	run.Status, run.TaskRequestKey = draftCreating, "agent-task-9001-0"
	expectConfirmationLock(mock, run)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_id = ?")).
		WithArgs(int64(888), run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).
		WithArgs("succeeded", run.ID, run.Scope.InitiatorID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	got, err := store.completeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.TaskRequestKey, 888)
	if err != nil || got.Status != draftSucceeded || got.TaskID != 888 || got.Draft != run.Draft {
		t.Fatalf("completion = %+v, %v", got, err)
	}
}

func TestCompleteDraftDoesNotOverwriteSuccess(t *testing.T) {
	for _, taskID := range []int64{888, 999} {
		t.Run(strconv.FormatInt(taskID, 10), func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			run.Status, run.TaskRequestKey, run.TaskID = draftSucceeded, "agent-task-9001-0", 888
			expectConfirmationLock(mock, run)
			if taskID == run.TaskID {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			got, err := store.completeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.TaskRequestKey, taskID)
			if taskID == run.TaskID && (err != nil || got != run) {
				t.Fatalf("repeated success = %+v, %v", got, err)
			}
			if taskID != run.TaskID && (got.ID != 0 || status.Code(err) != codes.Internal) {
				t.Fatalf("different result = %+v, %v", got, err)
			}
		})
	}
}

func TestCompleteDraftRollsBackResultWhenStateWriteFails(t *testing.T) {
	store, mock := testDraftStore(t)
	run := confirmationRun()
	run.Status, run.TaskRequestKey = draftCreating, "agent-task-9001-0"
	expectConfirmationLock(mock, run)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET task_id = ?")).
		WithArgs(int64(888), run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_runs SET status = ?")).
		WillReturnError(errors.New("database unavailable"))
	mock.ExpectRollback()
	got, err := store.completeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.TaskRequestKey, 888)
	if got.ID != 0 || status.Code(err) != codes.Unavailable {
		t.Fatalf("completion failure = %+v, %v", got, err)
	}
}

func TestConfirmationCannotAccessAnotherInitiatorAndEditCannotUnfreezeDraft(t *testing.T) {
	store, mock := testDraftStore(t)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator+" FOR UPDATE")).
		WithArgs(int64(9001), int64(401)).WillReturnRows(sqlmock.NewRows([]string{"initiator_id"}))
	mock.ExpectRollback()
	if _, err := store.freezeDraft(context.Background(), 9001, 401, "修复缓存", "复核", 1); status.Code(err) != codes.NotFound {
		t.Fatalf("other initiator = %v", err)
	}
	run := confirmationRun()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT r.status, d.title, d.description")).
		WithArgs(run.ID, run.Scope.InitiatorID).
		WillReturnRows(sqlmock.NewRows([]string{"status", "title", "description", "revision"}).AddRow("creating", "修复缓存", "复核", 1))
	mock.ExpectRollback()
	if err := store.updateDraftText(context.Background(), run, "changed", "changed"); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("edit after concurrent confirmation = %v", err)
	}
}

func TestReadDraftIncludesPersistedCreationResult(t *testing.T) {
	store, mock := testDraftStore(t)
	run := confirmationRun()
	run.Status, run.TaskRequestKey, run.TaskID = draftSucceeded, "agent-task-9001-0", 888
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator)).WithArgs(run.ID, run.Scope.InitiatorID).
		WillReturnRows(confirmationRows(run))
	got, err := store.loadDraftForInitiator(context.Background(), run.ID, run.Scope.InitiatorID)
	if err != nil || got != run {
		t.Fatalf("read completed = %+v, %v", got, err)
	}
}
