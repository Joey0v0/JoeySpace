package agent

import (
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewWaitingTaskDraftRunNormalizesOneDraft(t *testing.T) {
	scope := draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}
	run, err := newWaitingTaskDraftRun(scope, taskDraft{
		Title: " 修复缓存 ", Description: " 完成回归测试 ",
		AssigneeID: 500, DueAtUnixMs: maxDraftDueAtUnixMs, SourceMessageID: 600,
	})
	if err != nil || run.Scope != scope || run.Status != draftWaitingConfirmation ||
		run.Draft.Title != "修复缓存" || run.Draft.Description != "完成回归测试" ||
		run.Draft.AssigneeID != 500 || run.Draft.DueAtUnixMs != maxDraftDueAtUnixMs || run.Draft.SourceMessageID != 600 {
		t.Fatalf("draft run = %+v, %v", run, err)
	}
	if err := run.requireInitiator(400); err != nil {
		t.Fatal(err)
	}
	if err := run.requireWaitingConfirmation(400); err != nil {
		t.Fatal(err)
	}
}

func TestTaskDraftRejectsInvalidScopeAndFields(t *testing.T) {
	scope := draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}
	for _, tc := range []struct {
		name  string
		scope draftRunScope
		draft taskDraft
	}{
		{"missing team", draftRunScope{GroupID: 300, InitiatorID: 400}, taskDraft{Title: "valid"}},
		{"missing group", draftRunScope{TeamID: 200, InitiatorID: 400}, taskDraft{Title: "valid"}},
		{"missing initiator", draftRunScope{TeamID: 200, GroupID: 300}, taskDraft{Title: "valid"}},
		{"empty title", scope, taskDraft{Title: "  "}},
		{"long title", scope, taskDraft{Title: strings.Repeat("题", 201)}},
		{"long description", scope, taskDraft{Title: "valid", Description: strings.Repeat("a", 2001)}},
		{"invalid unicode", scope, taskDraft{Title: string([]byte{0xff})}},
		{"negative assignee", scope, taskDraft{Title: "valid", AssigneeID: -1}},
		{"negative source", scope, taskDraft{Title: "valid", SourceMessageID: -1}},
		{"negative due", scope, taskDraft{Title: "valid", DueAtUnixMs: -1}},
		{"late due", scope, taskDraft{Title: "valid", DueAtUnixMs: maxDraftDueAtUnixMs + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := newWaitingTaskDraftRun(tc.scope, tc.draft); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTaskDraftAccessRequiresInitiatorAndWaitingStatus(t *testing.T) {
	run, err := newWaitingTaskDraftRun(draftRunScope{TeamID: 200, GroupID: 300, InitiatorID: 400}, taskDraft{Title: "valid"})
	if err != nil {
		t.Fatal(err)
	}
	for _, actorID := range []int64{0, 401} {
		if err := run.requireInitiator(actorID); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("actor %d access error = %v", actorID, err)
		}
		if err := run.requireWaitingConfirmation(actorID); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("actor %d confirmation error = %v", actorID, err)
		}
	}
	run.Status = "succeeded"
	if err := run.requireWaitingConfirmation(400); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("completed run confirmation error = %v", err)
	}
}
