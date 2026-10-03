package agent

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDraftAssigneeReviewRequiresPresenceAndResolvesAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		draft    taskDraft
		reviewed *int64
		want     codes.Code
	}{
		{"legacy absent", taskDraft{AssigneeID: 500}, nil, codes.OK},
		{"none absent", taskDraft{AssigneeResolution: assigneeNone}, nil, codes.OK},
		{"matched missing", taskDraft{AssigneeName: "张三", AssigneeResolution: assigneeMatched, AssigneeID: 500}, nil, codes.FailedPrecondition},
		{"matched reviewed", taskDraft{AssigneeName: "张三", AssigneeResolution: assigneeMatched, AssigneeID: 500}, draftID(500), codes.OK},
		{"manual missing", taskDraft{AssigneeResolution: assigneeSelected, AssigneeID: 500}, nil, codes.FailedPrecondition},
		{"manual reviewed", taskDraft{AssigneeResolution: assigneeSelected, AssigneeID: 500}, draftID(500), codes.OK},
		{"clear missing", taskDraft{AssigneeResolution: assigneeUnassigned}, nil, codes.FailedPrecondition},
		{"clear explicit zero", taskDraft{AssigneeName: "张三", AssigneeResolution: assigneeUnassigned}, draftID(0), codes.OK},
		{"ambiguous zero reviewed", taskDraft{AssigneeName: "张三", AssigneeResolution: assigneeAmbiguous}, draftID(0), codes.FailedPrecondition},
		{"unresolved zero reviewed", taskDraft{AssigneeName: "张三", AssigneeResolution: assigneeNotFound}, draftID(0), codes.FailedPrecondition},
		{"truncated zero reviewed", taskDraft{AssigneeName: "张三", AssigneeResolution: assigneeTruncated}, draftID(0), codes.FailedPrecondition},
		{"stale legacy ID", taskDraft{AssigneeID: 500}, draftID(501), codes.Aborted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.draft.requireAssigneeReview(tc.reviewed); status.Code(err) != tc.want {
				t.Fatal(err)
			}
		})
	}
}

func TestFreezeDraftChecksReviewedAssigneeInsideTransaction(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reviewed *int64
		want     codes.Code
	}{
		{"missing", nil, codes.FailedPrecondition}, {"changed", draftID(501), codes.Aborted}, {"reviewed", draftID(500), codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			run.Draft.AssigneeResolution = assigneeSelected
			expectConfirmationLock(mock, run)
			if tc.want == codes.OK {
				mock.ExpectExec("UPDATE agent_task_drafts SET task_request_key").WithArgs("agent-task-9001-0", run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectExec("UPDATE agent_runs SET status").WithArgs("creating", run.ID, run.Scope.InitiatorID).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			got, err := store.freezeDraft(context.Background(), run.ID, run.Scope.InitiatorID, run.Draft.Title, run.Draft.Description, run.Revision, tc.reviewed)
			if status.Code(err) != tc.want || (tc.want == codes.OK && (got.Draft != run.Draft || got.Revision != 1 || got.Status != draftCreating)) {
				t.Fatalf("freeze: %+v %v", got, err)
			}
		})
	}
}

func TestConfirmSelectedAssigneeChecksOnlyWaitingTargetAndReusesFrozenResult(t *testing.T) {
	for _, state := range []draftRunStatus{draftWaitingConfirmation, draftCreating, draftSucceeded} {
		t.Run(string(state), func(t *testing.T) {
			run := confirmationRun()
			run.Status = state
			run.Draft.AssigneeName = "张三"
			run.Draft.AssigneeResolution = assigneeSelected
			if state != draftWaitingConfirmation {
				run.TaskRequestKey = draftTaskRequestKey(run.ID)
			}
			if state == draftSucceeded {
				run.TaskID = 123
			}
			reader := confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
			var memberCalls, taskCalls int
			reader.members = draftMemberFunc(func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
				memberCalls++
				return nil, status.Error(codes.NotFound, "target left or disabled")
			})
			confirmer := &draftConfirmer{store: confirmationStoreStub{
				freeze: func(context.Context, int64, int64, string, string) (taskDraftRun, error) { return run, nil },
				complete: func(context.Context, int64, int64, string, int64) (taskDraftRun, error) {
					run.Status, run.TaskID = draftSucceeded, 123
					return run, nil
				},
			}, tasks: draftTaskCreateFunc(func(_ context.Context, req *taskpb.CreateTaskRequest) (*taskpb.CreateTaskResponse, error) {
				taskCalls++
				if req.AssigneeId != 500 {
					t.Error("frozen assignee changed")
				}
				return &taskpb.CreateTaskResponse{TaskId: 123}, nil
			})}
			req := confirmDraftRequest()
			req.ExpectedAssigneeId = draftID(500)
			ctx, cancel := draftRPCContext()
			defer cancel()
			got, err := testTaskDraftClient(t, reader, confirmer).ConfirmTaskDraft(ctx, req)
			if state == draftWaitingConfirmation {
				if status.Code(err) != codes.NotFound || memberCalls != 1 || taskCalls != 0 {
					t.Fatalf("waiting: %v %v %d %d", got, err, memberCalls, taskCalls)
				}
			} else {
				expectedTaskCalls := 0
				if state == draftCreating {
					expectedTaskCalls = 1
				}
				if err != nil || got.GetTaskId() != 123 || memberCalls != 0 || taskCalls != expectedTaskCalls {
					t.Fatalf("frozen retry: %v %v %d %d", got, err, memberCalls, taskCalls)
				}
			}
		})
	}
}

func TestSelectedAssigneeSurvivesTextEditWithNewRevision(t *testing.T) {
	run := confirmationRun()
	run.Draft.AssigneeName = "张三"
	run.Draft.AssigneeResolution = assigneeSelected
	reader := confirmationReader(t, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, nil)
	reader.editor = draftTextUpdateFunc(func(context.Context, taskDraftRun, string, string) error { return nil })
	got, err := reader.editText(context.Background(), "user-token", run.ID, run.Draft.Title, run.Draft.Description, "新标题", run.Draft.Description, 1)
	if err != nil || got.Revision != 2 || got.Draft.AssigneeResolution != assigneeSelected || got.Draft.AssigneeID != 500 || got.Draft.AssigneeName != "张三" {
		t.Fatalf("edit: %+v %v", got, err)
	}
}
