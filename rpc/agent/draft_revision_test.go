package agent

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDraftRevisionNoOpAndOverflowBeforeWriting(t *testing.T) {
	for _, tc := range []struct {
		revision int64
		change   bool
		want     codes.Code
	}{{7, false, codes.OK}, {int64(1<<63 - 1), true, codes.FailedPrecondition}} {
		run := editableRun()
		run.Revision = tc.revision
		reader := testDraftAccessReader(t, run.Scope.InitiatorID,
			func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil },
			func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return nil })
		writes := 0
		reader.editor = draftTextUpdateFunc(func(context.Context, taskDraftRun, string, string) error {
			writes++
			return nil
		})
		title := run.Draft.Title
		if tc.change {
			title = "Changed title"
		}
		result, err := reader.editText(context.Background(), "user-token", run.ID, run.Draft.Title, run.Draft.Description, title, run.Draft.Description, run.Revision)
		if status.Code(err) != tc.want || (tc.change && writes != 0) || (!tc.change && result.Revision != run.Revision) {
			t.Fatalf("no-op/overflow: %+v, %v, writes=%d", result, err, writes)
		}
	}
}

func TestDraftRevisionRejectsRestoredTextAndChangedAssignee(t *testing.T) {
	for _, changeAssignee := range []bool{false, true} {
		store, mock := testDraftStore(t)
		run := confirmationRun()
		run.Revision = 3 // Another window changed then restored the visible text.
		if changeAssignee {
			run.Draft.AssigneeID = 501
		}
		expectConfirmationLock(mock, run)
		mock.ExpectRollback()
		if _, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, 1); status.Code(err) != codes.Aborted {
			t.Fatalf("stale content accepted: %v", err)
		}
	}
}

func TestDraftRevisionEditChecksLockedVersionAndNoOpDoesNotWrite(t *testing.T) {
	for _, tc := range []struct {
		revision int64
		want     codes.Code
	}{{1, codes.OK}, {3, codes.Aborted}} {
		store, mock := testDraftStore(t)
		run := editableRun()
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT r.status, d.title, d.description, d.revision FROM agent_runs")).WithArgs(run.ID, run.Scope.InitiatorID).
			WillReturnRows(sqlmock.NewRows([]string{"status", "title", "description", "revision"}).AddRow("waiting_confirmation", run.Draft.Title, run.Draft.Description, tc.revision))
		if tc.want == codes.OK {
			mock.ExpectCommit()
		} else {
			mock.ExpectRollback()
		}
		err := store.updateDraftText(context.Background(), run, run.Draft.Title, run.Draft.Description)
		if status.Code(err) != tc.want {
			t.Fatalf("no-op or stale edit: %v", err)
		}
	}
}

func TestDraftRevisionMissingWritesRejectedBeforeAccess(t *testing.T) {
	client := testTaskDraftClient(t, nil)
	ctx, cancel := draftRPCContext()
	defer cancel()
	edit := editDraftRequest()
	edit.ExpectedRevision = 0
	if _, err := client.EditTaskDraft(ctx, edit); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing edit revision: %v", err)
	}
	confirm := confirmDraftRequest()
	confirm.ExpectedRevision = 0
	if _, err := client.ConfirmTaskDraft(ctx, confirm); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing confirmation revision: %v", err)
	}
}

func TestFrozenDraftRevisionIsStableAcrossReplays(t *testing.T) {
	for _, state := range []draftRunStatus{draftCreating, draftSucceeded} {
		store, mock := testDraftStore(t)
		run := confirmationRun()
		run.Revision = 4
		run.Status = state
		run.TaskRequestKey = draftTaskRequestKey(run.ID)
		if state == draftSucceeded {
			run.TaskID = 77
		}
		expectConfirmationLock(mock, run)
		mock.ExpectCommit()
		result, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, 4)
		if err != nil || result.Revision != 4 || result.TaskRequestKey != run.TaskRequestKey || result.TaskID != run.TaskID {
			t.Fatalf("frozen version changed: %+v, %v", result, err)
		}
	}
}

func TestDraftRevisionMigrationAndInvalidPersistedVersion(t *testing.T) {
	init, err := os.ReadFile("../../deploy/mysql/init.sql")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../deploy/mysql/migrations/017_agent_draft_revision.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{string(init), string(migration)} {
		if !strings.Contains(strings.Join(strings.Fields(content), " "), "revision BIGINT NOT NULL DEFAULT 1") {
			t.Fatal("old draft default version missing")
		}
	}
	store, mock := testDraftStore(t)
	run := confirmationRun()
	run.Revision = 0
	mock.ExpectQuery(regexp.QuoteMeta(selectDraftForInitiator)).WithArgs(run.ID, run.Scope.InitiatorID).WillReturnRows(confirmationRows(run))
	if _, err := store.loadDraftForInitiator(context.Background(), run.ID, run.Scope.InitiatorID); status.Code(err) != codes.Unavailable {
		t.Fatalf("invalid persisted version accepted: %v", err)
	}
}
