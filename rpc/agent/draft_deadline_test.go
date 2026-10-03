package agent

import (
	"context"
	"regexp"
	"strconv"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestDraftDeadlineEditChangesOnlyDeadlineAndRevision(t *testing.T) {
	for _, due := range []int64{0, 1000, 1791097200000, maxDraftDueAtUnixMs} {
		t.Run(strconv.FormatInt(due, 10), func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			run.Draft.AssigneeName, run.Draft.AssigneeResolution = "张三", assigneeSelected
			expectConfirmationLock(mock, run)
			if due != run.Draft.DueAtUnixMs {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET due_at_unix_ms = ?, revision = revision + 1")).
					WithArgs(due, run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			got, err := store.updateDraftDeadline(context.Background(), run, due)
			want := run
			want.Draft.DueAtUnixMs = due
			if due != run.Draft.DueAtUnixMs {
				want.Revision++
			}
			if err != nil || got != want {
				t.Fatalf("edit: %+v %v; want %+v", got, err, want)
			}
		})
	}
}

func TestDraftDeadlineEditProtectsCompleteLockedDraft(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskDraftRun)
		want   codes.Code
	}{
		{"new version", func(r *taskDraftRun) { r.Revision++ }, codes.Aborted},
		{"assignee changed", func(r *taskDraftRun) { r.Draft.AssigneeID++ }, codes.Aborted},
		{"time changed", func(r *taskDraftRun) { r.Draft.DueAtUnixMs++ }, codes.Aborted},
		{"scope changed", func(r *taskDraftRun) { r.Scope.TeamID++ }, codes.Aborted},
		{"creating", func(r *taskDraftRun) { r.Status = draftCreating }, codes.FailedPrecondition},
		{"succeeded", func(r *taskDraftRun) { r.Status = draftSucceeded }, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			locked := run
			tc.mutate(&locked)
			expectConfirmationLock(mock, locked)
			mock.ExpectRollback()
			if _, err := store.updateDraftDeadline(context.Background(), run, 0); status.Code(err) != tc.want {
				t.Fatal(err)
			}
		})
	}
}

func TestDraftDeadlineRevisionLimitAllowsNoopAndRejectsChanges(t *testing.T) {
	for _, due := range []int64{1000, 0} {
		t.Run(strconv.FormatInt(due, 10), func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			run.Revision = int64(1<<63 - 1)
			expectConfirmationLock(mock, run)
			if due == 1000 {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			got, err := store.updateDraftDeadline(context.Background(), run, due)
			if due == 1000 {
				if err != nil || got != run {
					t.Fatalf("noop: %+v %v", got, err)
				}
			} else if status.Code(err) != codes.FailedPrecondition {
				t.Fatal(err)
			}
		})
	}
}

func TestDraftDeadlineRPCRequiresExplicitValidValue(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer user-token"))
	for _, req := range []*pb.EditTaskDraftDeadlineRequest{
		{RunId: 9001, ExpectedRevision: 1},
		{RunId: 9001, ExpectedRevision: 1, DueAtUnixMs: draftID(-1)},
		{RunId: 9001, ExpectedRevision: 1, DueAtUnixMs: draftID(maxDraftDueAtUnixMs + 1)},
		{RunId: 9001, DueAtUnixMs: draftID(0)},
	} {
		if _, err := (&Server{}).EditTaskDraftDeadline(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
	if _, err := (&Server{}).EditTaskDraftDeadline(context.Background(), &pb.EditTaskDraftDeadlineRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
}

func TestDraftDeadlineRPCChecksOwnerGroupAndRevisionBeforeWrite(t *testing.T) {
	for _, tc := range []struct {
		name            string
		actor, revision int64
		groupErr        error
		state           draftRunStatus
		want            codes.Code
	}{
		{"valid clear", 400, 1, nil, draftWaitingConfirmation, codes.OK},
		{"other owner", 401, 1, nil, draftWaitingConfirmation, codes.NotFound},
		{"left group", 400, 1, status.Error(codes.PermissionDenied, "left"), draftWaitingConfirmation, codes.PermissionDenied},
		{"old version", 400, 2, nil, draftWaitingConfirmation, codes.Aborted},
		{"frozen", 400, 1, nil, draftCreating, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := confirmationRun()
			run.Status = tc.state
			store, mock := testDraftStore(t)
			reader := testDraftAccessReader(t, tc.actor, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return tc.groupErr })
			reader.deadline = store
			if tc.want == codes.OK {
				expectConfirmationLock(mock, run)
				mock.ExpectExec("UPDATE agent_task_drafts SET due_at_unix_ms").WithArgs(int64(0), run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			ctx, cancel := draftRPCContext()
			defer cancel()
			got, err := testTaskDraftClient(t, reader).EditTaskDraftDeadline(ctx, &pb.EditTaskDraftDeadlineRequest{RunId: run.ID, ExpectedRevision: tc.revision, DueAtUnixMs: draftID(0)})
			if status.Code(err) != tc.want || (tc.want == codes.OK && (got.GetDraft().GetDueAtUnixMs() != 0 || got.GetDraft().GetRevision() != 2 || got.GetDraft().GetAssigneeId() != 500)) {
				t.Fatalf("RPC: %v %v", got, err)
			}
		})
	}
}

func TestFreezeDraftDeadlineReviewAndFrozenReplay(t *testing.T) {
	for _, state := range []draftRunStatus{draftWaitingConfirmation, draftCreating, draftSucceeded} {
		for _, tc := range []struct {
			name     string
			due      int64
			reviewed *int64
			want     codes.Code
		}{
			{"legacy zero absent", 0, nil, codes.OK},
			{"explicit zero", 0, draftID(0), codes.OK},
			{"nonzero absent", 1000, nil, codes.FailedPrecondition},
			{"changed", 1000, draftID(1001), codes.Aborted},
			{"reviewed", 1000, draftID(1000), codes.OK},
		} {
			t.Run(string(state)+"/"+tc.name, func(t *testing.T) {
				store, mock := testDraftStore(t)
				run := confirmationRun()
				run.Status = state
				run.Draft.DueAtUnixMs = tc.due
				if state != draftWaitingConfirmation {
					run.TaskRequestKey = draftTaskRequestKey(run.ID)
				}
				if state == draftSucceeded {
					run.TaskID = 123
				}
				expectConfirmationLock(mock, run)
				if tc.want == codes.OK {
					if state == draftWaitingConfirmation {
						mock.ExpectExec("UPDATE agent_task_drafts SET task_request_key").WithArgs(draftTaskRequestKey(run.ID), run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
						mock.ExpectExec("UPDATE agent_runs SET status").WithArgs("creating", run.ID, run.Scope.InitiatorID).WillReturnResult(sqlmock.NewResult(0, 1))
					}
					mock.ExpectCommit()
				} else {
					mock.ExpectRollback()
				}
				got, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, run.Revision, nil, tc.reviewed)
				if status.Code(err) != tc.want || (tc.want == codes.OK && (got.Draft != run.Draft || got.Revision != run.Revision)) {
					t.Fatalf("freeze: %+v %v", got, err)
				}
			})
		}
	}
}

func TestConfirmDraftDeadlineReviewRejectsBeforeTask(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reviewed *int64
		want     codes.Code
	}{
		{"missing", nil, codes.FailedPrecondition}, {"old value", draftID(0), codes.Aborted}, {"negative", draftID(-1), codes.InvalidArgument}, {"overflow", draftID(maxDraftDueAtUnixMs + 1), codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := confirmationRun()
			reader := confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
			confirmer := &draftConfirmer{store: confirmationStoreStub{freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) {
				t.Fatal("unreviewed deadline frozen")
				return run, nil
			}}, tasks: draftTaskCreateFunc(nil)}
			req := confirmDraftRequest()
			req.ExpectedDueAtUnixMs = tc.reviewed
			ctx, cancel := draftRPCContext()
			defer cancel()
			if _, err := testTaskDraftClient(t, reader, confirmer).ConfirmTaskDraft(ctx, req); status.Code(err) != tc.want {
				t.Fatal(err)
			}
		})
	}
}
