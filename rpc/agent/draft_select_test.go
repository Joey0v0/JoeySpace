package agent

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type draftMemberFunc func(context.Context, *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error)

func (f draftMemberFunc) CheckTeamMemberByID(ctx context.Context, req *userpb.CheckTeamMemberByIDRequest, _ ...grpc.CallOption) (*userpb.CheckTeamMemberByIDResponse, error) {
	return f(ctx, req)
}
func draftID(id int64) *int64 { return &id }

func TestSelectDraftAssigneeTransactionChangesAndNoop(t *testing.T) {
	for _, tc := range []struct {
		name          string
		old           draftAssigneeResolution
		oldID, target int64
		change        bool
	}{
		{"unique becomes manual", assigneeMatched, 500, 500, true},
		{"same manual choice", assigneeSelected, 500, 500, false},
		{"ambiguity explicitly cleared", assigneeAmbiguous, 0, 0, true},
		{"same unassigned", assigneeUnassigned, 0, 0, false},
		{"different member", assigneeSelected, 500, 501, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			run.Draft.AssigneeName = "张三"
			run.Draft.AssigneeResolution = tc.old
			run.Draft.AssigneeID = tc.oldID
			expectConfirmationLock(mock, run)
			resolution := assigneeSelected
			if tc.target == 0 {
				resolution = assigneeUnassigned
			}
			if tc.change {
				mock.ExpectExec(regexp.QuoteMeta("UPDATE agent_task_drafts SET assignee_id = ?, assignee_resolution = ?, revision = revision + 1")).WithArgs(tc.target, string(resolution), run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			got, err := store.updateDraftAssignee(context.Background(), run, tc.target)
			expectedRevision := run.Revision
			if tc.change {
				expectedRevision++
			}
			if err != nil || got.Revision != expectedRevision || got.Draft.AssigneeID != tc.target || got.Draft.AssigneeResolution != resolution || got.Draft.AssigneeName != "张三" || got.Draft.Title != run.Draft.Title {
				t.Fatalf("choice: %+v, %v", got, err)
			}
		})
	}
}

func TestSelectDraftAssigneeRejectsStaleOrFrozenTransaction(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*taskDraftRun)
		want   codes.Code
	}{
		{"new revision", func(r *taskDraftRun) { r.Revision++ }, codes.Aborted},
		{"frozen", func(r *taskDraftRun) { r.Status = draftCreating }, codes.FailedPrecondition},
		{"changed scope", func(r *taskDraftRun) { r.Scope.TeamID++ }, codes.Aborted},
		{"invalid stored draft", func(r *taskDraftRun) { r.Draft.AssigneeResolution = "unknown" }, codes.Aborted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, mock := testDraftStore(t)
			run := confirmationRun()
			locked := run
			tc.mutate(&locked)
			expectConfirmationLock(mock, locked)
			mock.ExpectRollback()
			_, err := store.updateDraftAssignee(context.Background(), run, 500)
			if status.Code(err) != tc.want {
				t.Fatal(err)
			}
		})
	}
}

func TestSelectDraftAssigneeChecksOwnerScopeAndCurrentMemberBeforeTransaction(t *testing.T) {
	for _, tc := range []struct {
		name                string
		actor               int64
		groupErr, memberErr error
		want                codes.Code
	}{
		{"active", 400, nil, nil, codes.OK},
		{"left group", 400, status.Error(codes.PermissionDenied, "left"), nil, codes.PermissionDenied},
		{"other owner", 401, nil, nil, codes.NotFound},
		{"disabled or left member", 400, nil, status.Error(codes.NotFound, "inactive"), codes.NotFound},
		{"member dependency down", 400, nil, status.Error(codes.Unimplemented, "private"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := confirmationRun()
			store, mock := testDraftStore(t)
			reader := testDraftAccessReader(t, tc.actor, func(context.Context, int64, int64) (taskDraftRun, error) { return run, nil }, func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return tc.groupErr })
			reader.selector = store
			reader.members = draftMemberFunc(func(ctx context.Context, req *userpb.CheckTeamMemberByIDRequest) (*userpb.CheckTeamMemberByIDResponse, error) {
				md, _ := metadata.FromOutgoingContext(ctx)
				if req.TeamId != 200 || req.UserId != 501 || md.Get("authorization")[0] != "Bearer user-token" {
					t.Errorf("member scope: %v %v", req, md)
				}
				return &userpb.CheckTeamMemberByIDResponse{}, tc.memberErr
			})
			if tc.want == codes.OK {
				expectConfirmationLock(mock, run)
				mock.ExpectExec("UPDATE agent_task_drafts SET assignee_id").WithArgs(int64(501), "selected", run.ID).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			ctx, cancel := draftRPCContext()
			defer cancel()
			got, err := testTaskDraftClient(t, reader, nil).SelectTaskDraftAssignee(ctx, &pb.SelectTaskDraftAssigneeRequest{RunId: run.ID, AssigneeId: draftID(501), ExpectedRevision: 1})
			if status.Code(err) != tc.want {
				t.Fatalf("select: %v %v", got, err)
			}
		})
	}
}

func TestSelectDraftAssigneeRequiresPresentNonnegativeIDAndVersion(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer user-token"))
	for _, req := range []*pb.SelectTaskDraftAssigneeRequest{
		{RunId: 9001, ExpectedRevision: 1}, {RunId: 9001, ExpectedRevision: 1, AssigneeId: draftID(-1)}, {RunId: 9001, AssigneeId: draftID(0)},
	} {
		if _, err := (&Server{}).SelectTaskDraftAssignee(ctx, req); status.Code(err) != codes.InvalidArgument {
			t.Fatal(err)
		}
	}
}
