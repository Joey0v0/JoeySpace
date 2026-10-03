package agent

import (
	"context"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func batchVerificationInputs(t *testing.T) (draftRunScope, string, []*impb.TeamGroupMessage, []taskDraft, int64) {
	t.Helper()
	return draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, "李四明天下午整理文档", []*impb.TeamGroupMessage{
			{Id: 9007199254740993, ContentType: 1, Content: "张三修复缓存", CreatedAtUnixMs: deadlineTestUTC(t, "2026-10-01T04:00:00Z")},
			{Id: 9007199254740995, ContentType: 1, Content: "明天 15:30", CreatedAtUnixMs: deadlineTestUTC(t, "2026-10-03T04:00:00Z")},
		}, []taskDraft{
			{Title: "修复缓存", SourceMessageID: 9007199254740993, AssigneeName: "张三", Deadline: draftDeadlineMetadata{Text: "明天 15:30", Source: "message", SourceMessageID: 9007199254740995}},
			{Title: "整理文档", SourceMessageID: 9007199254740995, AssigneeName: "李四", Deadline: draftDeadlineMetadata{Text: "明天下午", Source: "instruction"}},
		}, deadlineTestUTC(t, "2026-10-06T16:00:00Z")
}

// Only the read-only member resolver is configured. Unexpected use of identity,
// messages, generation, SQL/Task or reply plumbing would fail this fixture.
func batchVerificationPreparer(t *testing.T, rejectSecond bool) *draftPreparer {
	t.Helper()
	return &draftPreparer{assignees: &draftAssigneeResolver{users: assigneeClientFunc(func(ctx context.Context, req *userpb.ResolveTeamMemberRequest) (*userpb.ResolveTeamMemberResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || !reflect.DeepEqual(md.Get("authorization"), []string{"Bearer user-token"}) {
			t.Fatalf("lookup scope = %+v, %v", req, md)
		}
		var id int64
		switch req.GetName() {
		case "张三":
			id = 501
		case "李四":
			if rejectSecond {
				return nil, status.Error(codes.PermissionDenied, "left team")
			}
			id = 502
		default:
			t.Fatalf("unexpected mention: %q", req.GetName())
		}
		return &userpb.ResolveTeamMemberResponse{Candidates: []*userpb.TeamMember{{UserId: id, Nickname: req.GetName()}}}, nil
	})}}
}

func TestDraftBatchVerifiesIndependentMembersSourcesAndFixedReferences(t *testing.T) {
	scope, instruction, messages, candidates, reference := batchVerificationInputs(t)
	before := append([]taskDraft(nil), candidates...)
	p := batchVerificationPreparer(t, false)
	drafts, err := p.verifyGeneratedDrafts(context.Background(), "user-token", scope, instruction, messages, candidates, &reference)
	if err != nil || len(drafts) != 2 {
		t.Fatalf("verified = %+v, %v", drafts, err)
	}
	if !reflect.DeepEqual(before, candidates) {
		t.Fatalf("input was modified: %+v", candidates)
	}
	if drafts[0].AssigneeID != 501 || drafts[1].AssigneeID != 502 || drafts[0].AssigneeResolution != assigneeMatched || drafts[1].AssigneeResolution != assigneeMatched {
		t.Fatalf("member resolution = %+v", drafts)
	}
	first, second := drafts[0], drafts[1]
	if first.SourceMessageID != 9007199254740993 || first.Deadline.SourceMessageID != 9007199254740995 || first.Deadline.ReferenceUnixMs != messages[1].GetCreatedAtUnixMs() || first.Deadline.InstructionReferenceUnixMs != reference || first.DueAtUnixMs != deadlineTestUTC(t, "2026-10-04T07:30:00Z") || first.Deadline.Resolution != "parsed" {
		t.Fatalf("message time evidence = %+v", first)
	}
	if second.SourceMessageID != 9007199254740995 || second.Deadline.Source != "instruction" || second.Deadline.SourceMessageID != 0 || second.Deadline.ReferenceUnixMs != reference || second.Deadline.InstructionReferenceUnixMs != reference || second.Deadline.Resolution != "needs_input" || second.Deadline.Reason != "unsupported_expression" || second.DueAtUnixMs != 0 {
		t.Fatalf("ambiguous time evidence = %+v", second)
	}
	again, err := p.verifyGeneratedDrafts(context.Background(), "user-token", scope, instruction, messages, candidates, &reference)
	if err != nil || !reflect.DeepEqual(drafts, again) {
		t.Fatalf("fixed-reference repeat = %+v, %v", again, err)
	}
	drafts[1].Deadline.Text = "changed locally"
	if drafts[0] != first || candidates[1] != before[1] {
		t.Fatal("one item changed another item's evidence")
	}
}

func TestDraftBatchSecondItemFailureReturnsNoPartialCollection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*taskDraft)
		denied bool
		code   codes.Code
	}{
		{"invalid task source", func(d *taskDraft) { d.SourceMessageID = 99 }, false, codes.FailedPrecondition},
		{"invented name", func(d *taskDraft) { d.AssigneeName = "王五" }, false, codes.FailedPrecondition},
		{"invented time", func(d *taskDraft) { d.Deadline.Text = "明天09:00" }, false, codes.FailedPrecondition},
		{"model member id", func(d *taskDraft) { d.AssigneeID = 502 }, false, codes.FailedPrecondition},
		{"model due", func(d *taskDraft) { d.DueAtUnixMs = 123 }, false, codes.FailedPrecondition},
		{"model time state", func(d *taskDraft) { d.Deadline.Resolution = "parsed" }, false, codes.FailedPrecondition},
		{"invalid content", func(d *taskDraft) { d.Title = "" }, false, codes.FailedPrecondition},
		{"membership denial", func(*taskDraft) {}, true, codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, instruction, messages, drafts, ref := batchVerificationInputs(t)
			tc.modify(&drafts[1])
			verified, err := batchVerificationPreparer(t, tc.denied).verifyGeneratedDrafts(context.Background(), "user-token", scope, instruction, messages, drafts, &ref)
			if verified != nil || status.Code(err) != tc.code {
				t.Fatalf("partial collection = %+v, %v", verified, err)
			}
		})
	}
}

func TestDraftBatchBoundsContextAndOptionalReference(t *testing.T) {
	scope, instruction, messages, drafts, ref := batchVerificationInputs(t)
	p := batchVerificationPreparer(t, false)
	for _, count := range []int{0, 1, 5, 6} {
		items := make([]taskDraft, count)
		for i := range items {
			items[i] = drafts[0]
		}
		verified, err := p.verifyGeneratedDrafts(context.Background(), "user-token", scope, instruction, messages, items, &ref)
		if count == 0 || count == 6 {
			if verified != nil || status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("count %d: %+v, %v", count, verified, err)
			}
		} else if err != nil || len(verified) != count {
			t.Fatalf("count %d: %+v, %v", count, verified, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if verified, err := p.verifyGeneratedDrafts(ctx, "user-token", scope, instruction, messages, drafts, &ref); verified != nil || status.Code(err) != codes.Canceled {
		t.Fatalf("canceled = %+v, %v", verified, err)
	}
	drafts[1].Deadline.Text = "明天 15:30"
	verified, err := p.verifyGeneratedDrafts(context.Background(), "user-token", scope, "李四明天 15:30整理文档", messages, drafts, nil)
	if err != nil || len(verified) != 2 || verified[0].Deadline.Resolution != "parsed" || verified[1].Deadline.Resolution != "needs_input" || verified[1].Deadline.Reason != "missing_reference" || verified[1].DueAtUnixMs != 0 {
		t.Fatalf("missing instruction reference = %+v, %v", verified, err)
	}
	drafts[1].Deadline.Text = "明天15:30"
	verified, err = p.verifyGeneratedDrafts(context.Background(), "user-token", scope, "李四明天15:30整理文档", messages, drafts, &ref)
	if err != nil || len(verified) != 2 || verified[0].Deadline.Resolution != "parsed" || verified[1].Deadline.Text != "明天15:30" || verified[1].Deadline.Resolution != "needs_input" || verified[1].Deadline.Reason != "unsupported_expression" || verified[1].DueAtUnixMs != 0 {
		t.Fatalf("unsupported compact expression = %+v, %v", verified, err)
	}
}

func TestDraftBatchEinoGenerationThenReadOnlyVerification(t *testing.T) {
	scope, instruction, messages, _, ref := batchVerificationInputs(t)
	second := batchModelItem()
	second["title"], second["assignee_name"], second["source_message_id"] = "整理文档", "李四", "9007199254740995"
	second["deadline_text"], second["deadline_source"], second["deadline_source_message_id"] = "明天下午", "instruction", "0"
	generator, err := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage(batchModelJSON(t, batchModelItem(), second), nil), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := generator.GenerateDrafts(context.Background(), instruction, messages)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := batchVerificationPreparer(t, false).verifyGeneratedDrafts(context.Background(), "user-token", scope, instruction, messages, candidates, &ref)
	if err != nil || len(verified) != 2 || verified[0].AssigneeID != 501 || verified[0].Deadline.Resolution != "parsed" || verified[1].AssigneeID != 502 || verified[1].Deadline.Resolution != "needs_input" {
		t.Fatalf("Eino -> verification = %+v, %v", verified, err)
	}
}
