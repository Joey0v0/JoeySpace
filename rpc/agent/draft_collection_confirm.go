package agent

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func draftCollectionTaskRequestKey(runID int64, index int32) string {
	return "agent-task-" + strconv.FormatInt(runID, 10) + "-" + strconv.FormatInt(int64(index), 10)
}

func validCollectionItemTaskState(run taskDraftRun, index int32) bool {
	switch run.Status {
	case draftWaitingConfirmation, draftSkipped:
		return run.TaskRequestKey == "" && run.TaskID == 0
	case draftCreating, draftSucceeded:
		if run.TaskRequestKey != draftCollectionTaskRequestKey(run.ID, index) || (run.Status == draftCreating && run.TaskID != 0) || (run.Status == draftSucceeded && run.TaskID <= 0) {
			return false
		}
		return run.Draft.requireAssigneeReview(&run.Draft.AssigneeID) == nil && run.Draft.requireDeadlineStateReview(&run.Draft.DueAtUnixMs, run.Draft.Deadline.Resolution) == nil
	}
	return false
}

type draftCollectionConfirmationReview struct {
	Revision           int64
	Title              string
	Description        string
	AssigneeID         int64
	DueAtUnixMs        int64
	DeadlineResolution string
}

func (r draftCollectionConfirmationReview) valid() bool {
	return r.Revision > 0 && utf8.ValidString(r.Title) && utf8.RuneCountInString(r.Title) >= 1 && utf8.RuneCountInString(r.Title) <= 200 && strings.TrimSpace(r.Title) == r.Title &&
		utf8.ValidString(r.Description) && utf8.RuneCountInString(r.Description) <= 2000 && strings.TrimSpace(r.Description) == r.Description &&
		r.AssigneeID >= 0 && r.DueAtUnixMs >= 0 && r.DueAtUnixMs <= maxDraftDueAtUnixMs && r.DeadlineResolution != "" && validDeadlineResolution(r.DeadlineResolution)
}

func (r draftCollectionConfirmationReview) require(run taskDraftRun) error {
	if run.Revision != r.Revision || run.Draft.Title != r.Title || run.Draft.Description != r.Description {
		return status.Error(codes.Aborted, "draft item changed; reload before confirming")
	}
	if err := run.Draft.requireAssigneeReview(&r.AssigneeID); err != nil {
		return err
	}
	return run.Draft.requireDeadlineStateReview(&r.DueAtUnixMs, r.DeadlineResolution)
}

func sameCollectionItemContent(left, right taskDraftRun) bool {
	return left.ID == right.ID && left.Scope == right.Scope && left.Revision == right.Revision && left.Draft == right.Draft
}

// Status may advance during a same-content concurrent confirmation; it cannot
// go backwards or replace an already recorded successful task result.
func validCollectionConfirmationProgress(before, after taskDraftRun) bool {
	if !sameCollectionItemContent(before, after) {
		return false
	}
	switch before.Status {
	case draftWaitingConfirmation:
		return after.Status == draftWaitingConfirmation || after.Status == draftCreating || after.Status == draftSucceeded
	case draftCreating:
		return (after.Status == draftCreating || after.Status == draftSucceeded) && before.TaskRequestKey == after.TaskRequestKey
	case draftSucceeded:
		return after.Status == draftSucceeded && before.TaskRequestKey == after.TaskRequestKey && before.TaskID == after.TaskID
	}
	return false
}
