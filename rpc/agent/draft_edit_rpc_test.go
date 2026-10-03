package agent

import (
	"context"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func editDraftRequest() *pb.EditTaskDraftRequest {
	return &pb.EditTaskDraftRequest{ExpectedRevision: 1,
		RunId: 9001, ExpectedTitle: "旧标题", ExpectedDescription: "旧说明",
		Title: " 新标题 ", Description: " 新说明 ",
	}
}

func TestEditTaskDraftOverRPCUsesCurrentAccessAndReturnsUpdatedText(t *testing.T) {
	reader := testDraftAccessReader(t, 400,
		func(_ context.Context, runID, actorID int64) (taskDraftRun, error) {
			if runID != 9001 || actorID != 400 {
				t.Fatalf("draft load scope = %d, %d", runID, actorID)
			}
			return editableRun(), nil
		},
		func(_ context.Context, req *impb.CheckTeamGroupAccessRequest) error {
			if req.GetTeamId() != 200 || req.GetGroupId() != 300 {
				t.Fatalf("IM scope = %v", req)
			}
			return nil
		})
	reader.editor = draftTextUpdateFunc(func(_ context.Context, run taskDraftRun, title, description string) error {
		if run.ID != 9001 || run.Draft.Title != "旧标题" || title != "新标题" || description != "新说明" {
			t.Fatalf("stored edit = %+v, %q, %q", run, title, description)
		}
		return nil
	})
	ctx, cancel := draftRPCContext()
	defer cancel()
	resp, err := testTaskDraftClient(t, reader).EditTaskDraft(ctx, editDraftRequest())
	if err != nil || resp.GetRunId() != 9001 || resp.GetTeamId() != 200 || resp.GetGroupId() != 300 ||
		resp.GetStatus() != "waiting_confirmation" || resp.GetDraft().GetTitle() != "新标题" ||
		resp.GetDraft().GetDescription() != "新说明" || resp.GetDraft().GetAssigneeId() != 500 ||
		resp.GetDraft().GetDueAtUnixMs() != 1000 || resp.GetDraft().GetSourceMessageId() != 600 || resp.GetDraft().GetRevision() != 2 {
		t.Fatalf("edit response = %v, %v", resp, err)
	}
}

func TestEditTaskDraftOverRPCRejectsMissingTokenInvalidIDAndMissingEditor(t *testing.T) {
	client := testTaskDraftClient(t, nil)
	if resp, err := client.EditTaskDraft(context.Background(), editDraftRequest()); resp != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token = %v, %v", resp, err)
	}
	ctx, cancel := draftRPCContext()
	defer cancel()
	if resp, err := client.EditTaskDraft(ctx, &pb.EditTaskDraftRequest{ExpectedRevision: 1}); resp != nil || status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid ID = %v, %v", resp, err)
	}
	if resp, err := client.EditTaskDraft(ctx, editDraftRequest()); resp != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("unconfigured edit = %v, %v", resp, err)
	}
}

func TestEditTaskDraftOverRPCRejectsStaleOrUnauthorizedEdit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		load  draftLoadFunc
		check draftAccessFunc
		req   *pb.EditTaskDraftRequest
		want  codes.Code
	}{
		{"other initiator", func(context.Context, int64, int64) (taskDraftRun, error) {
			return taskDraftRun{}, status.Error(codes.NotFound, "draft run not found")
		}, nil, editDraftRequest(), codes.NotFound},
		{"left group", func(context.Context, int64, int64) (taskDraftRun, error) { return editableRun(), nil },
			func(context.Context, *impb.CheckTeamGroupAccessRequest) error {
				return status.Error(codes.PermissionDenied, "left group")
			}, editDraftRequest(), codes.PermissionDenied},
		{"stale form", func(context.Context, int64, int64) (taskDraftRun, error) { return editableRun(), nil }, nil,
			&pb.EditTaskDraftRequest{ExpectedRevision: 1, RunId: 9001, ExpectedTitle: "更早的标题", ExpectedDescription: "旧说明", Title: "新标题"}, codes.Aborted},
		{"finished run", func(context.Context, int64, int64) (taskDraftRun, error) {
			run := editableRun()
			run.Status = "completed"
			return run, nil
		}, nil, editDraftRequest(), codes.FailedPrecondition},
		{"invalid title", func(context.Context, int64, int64) (taskDraftRun, error) { return editableRun(), nil }, nil,
			&pb.EditTaskDraftRequest{ExpectedRevision: 1, RunId: 9001, ExpectedTitle: "旧标题", ExpectedDescription: "旧说明", Title: " "}, codes.InvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := tc.check
			if check == nil {
				check = func(context.Context, *impb.CheckTeamGroupAccessRequest) error { return nil }
			}
			reader := testDraftAccessReader(t, 400, tc.load, check)
			reader.editor = draftTextUpdateFunc(func(context.Context, taskDraftRun, string, string) error {
				t.Fatal("rejected edit reached storage")
				return nil
			})
			ctx, cancel := draftRPCContext()
			defer cancel()
			resp, err := testTaskDraftClient(t, reader).EditTaskDraft(ctx, tc.req)
			if resp != nil || status.Code(err) != tc.want {
				t.Fatalf("rejected edit = %v, %v", resp, err)
			}
		})
	}
}
