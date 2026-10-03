package agent

import (
	"strings"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The Task RPC checks these limits again when a confirmed draft is created.
const maxDraftDueAtUnixMs int64 = 253402300799999

type draftRunStatus string

const (
	draftWaitingConfirmation draftRunStatus = "waiting_confirmation"
	draftCreating            draftRunStatus = "creating"
	draftSucceeded           draftRunStatus = "succeeded"
)

// draftRunScope must come from authenticated service context, never model output.
type draftRunScope struct {
	TeamID      int64
	GroupID     int64
	InitiatorID int64
}

// taskDraft contains one editable task. IDs are only candidates until the
// business services recheck assignee and source access at confirmation time.
type taskDraft struct {
	Title              string
	Description        string
	AssigneeID         int64
	DueAtUnixMs        int64
	SourceMessageID    int64
	AssigneeName       string
	AssigneeResolution draftAssigneeResolution
}

type draftAssigneeResolution string

const (
	assigneeNone      draftAssigneeResolution = "none"
	assigneeMatched   draftAssigneeResolution = "matched"
	assigneeNotFound  draftAssigneeResolution = "not_found"
	assigneeAmbiguous draftAssigneeResolution = "ambiguous"
	assigneeTruncated draftAssigneeResolution = "truncated"
)

func (d taskDraft) validAssigneeResolution() bool {
	if !utf8.ValidString(d.AssigneeName) || utf8.RuneCountInString(d.AssigneeName) > 64 || strings.TrimSpace(d.AssigneeName) != d.AssigneeName {
		return false
	}
	switch d.AssigneeResolution {
	case "": // Old persisted drafts predate name extraction; preserve their ID.
		return d.AssigneeName == ""
	case assigneeNone:
		return d.AssigneeName == "" && d.AssigneeID == 0
	case assigneeMatched:
		return d.AssigneeName != "" && d.AssigneeID > 0
	case assigneeNotFound, assigneeAmbiguous, assigneeTruncated:
		return d.AssigneeName != "" && d.AssigneeID == 0
	default:
		return false
	}
}

// The current confirmation contract only reviews text. Named drafts must wait
// for an explicit assignee review contract, even when the match is unique.
func (d taskDraft) requireTextOnlyConfirmation() error {
	if d.AssigneeName != "" || (d.AssigneeResolution != "" && d.AssigneeResolution != assigneeNone) {
		return status.Error(codes.FailedPrecondition, "assignee review is required before confirming this draft")
	}
	return nil
}

type taskDraftRun struct {
	ID             int64
	Revision       int64
	Scope          draftRunScope
	Status         draftRunStatus
	Draft          taskDraft
	TaskRequestKey string
	TaskID         int64
}

// newWaitingTaskDraftRun validates the shape of one draft before persistence.
// It does not authorize a user or call Task.CreateTask.
func newWaitingTaskDraftRun(scope draftRunScope, draft taskDraft) (taskDraftRun, error) {
	if scope.TeamID <= 0 || scope.GroupID <= 0 || scope.InitiatorID <= 0 {
		return taskDraftRun{}, status.Error(codes.InvalidArgument, "invalid draft scope")
	}
	draft.Title = strings.TrimSpace(draft.Title)
	draft.Description = strings.TrimSpace(draft.Description)
	if !utf8.ValidString(draft.Title) || utf8.RuneCountInString(draft.Title) < 1 || utf8.RuneCountInString(draft.Title) > 200 ||
		!utf8.ValidString(draft.Description) || utf8.RuneCountInString(draft.Description) > 2000 ||
		draft.AssigneeID < 0 || draft.SourceMessageID < 0 || !draft.validAssigneeResolution() ||
		draft.DueAtUnixMs < 0 || draft.DueAtUnixMs > maxDraftDueAtUnixMs {
		return taskDraftRun{}, status.Error(codes.InvalidArgument, "invalid task draft")
	}
	return taskDraftRun{Scope: scope, Status: draftWaitingConfirmation, Draft: draft, Revision: 1}, nil
}

// actorID must be resolved from the current Token by a trusted service.
// Current team and group membership still need RPC checks before access.
func (r taskDraftRun) requireInitiator(actorID int64) error {
	if actorID <= 0 || actorID != r.Scope.InitiatorID {
		return status.Error(codes.PermissionDenied, "draft belongs to another user")
	}
	return nil
}

func (r taskDraftRun) requireWaitingConfirmation(actorID int64) error {
	if err := r.requireInitiator(actorID); err != nil {
		return err
	}
	if r.Status != draftWaitingConfirmation {
		return status.Error(codes.FailedPrecondition, "draft is not awaiting confirmation")
	}
	return nil
}
