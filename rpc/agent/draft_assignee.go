package agent

import (
	"context"
	"strings"
	"unicode/utf8"

	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type draftAssigneeClient interface {
	ResolveTeamMember(context.Context, *userpb.ResolveTeamMemberRequest, ...grpc.CallOption) (*userpb.ResolveTeamMemberResponse, error)
}

type draftAssigneeResolver struct{ users draftAssigneeClient }

func (r *draftAssigneeResolver) resolve(ctx context.Context, token string, teamID int64, draft taskDraft) (taskDraft, error) {
	if draft.AssigneeID != 0 || draft.AssigneeResolution != "" || !utf8.ValidString(draft.AssigneeName) {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "invalid generated assignee")
	}
	draft.AssigneeName = strings.TrimSpace(draft.AssigneeName)
	if utf8.RuneCountInString(draft.AssigneeName) > 64 {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "invalid generated assignee")
	}
	if draft.AssigneeName == "" {
		draft.AssigneeResolution = assigneeNone
		return draft, nil
	}
	if r == nil || r.users == nil {
		return taskDraft{}, status.Error(codes.Unavailable, "assignee resolution is not configured")
	}
	readCtx, cancel, err := authorizedReadContext(ctx, token)
	if err != nil {
		return taskDraft{}, err
	}
	defer cancel()
	result, err := r.users.ResolveTeamMember(readCtx, &userpb.ResolveTeamMemberRequest{TeamId: teamID, Name: draft.AssigneeName})
	if readCtx.Err() != nil {
		return taskDraft{}, status.FromContextError(readCtx.Err()).Err()
	}
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound:
			return taskDraft{}, status.Error(status.Code(err), "assignee lookup rejected")
		default:
			return taskDraft{}, status.Error(codes.Unavailable, "assignee lookup unavailable")
		}
	}
	if result == nil || len(result.GetCandidates()) > 20 || (result.GetTruncated() && len(result.GetCandidates()) != 20) {
		return taskDraft{}, status.Error(codes.Unavailable, "invalid assignee lookup response")
	}
	var previousID int64
	for _, candidate := range result.GetCandidates() {
		if candidate.GetUserId() <= previousID || (candidate.GetUsername() != draft.AssigneeName && candidate.GetNickname() != draft.AssigneeName) {
			return taskDraft{}, status.Error(codes.Unavailable, "invalid assignee lookup response")
		}
		previousID = candidate.GetUserId()
	}
	switch {
	case result.GetTruncated():
		draft.AssigneeResolution = assigneeTruncated
	case len(result.GetCandidates()) == 0:
		draft.AssigneeResolution = assigneeNotFound
	case len(result.GetCandidates()) == 1:
		draft.AssigneeResolution = assigneeMatched
		draft.AssigneeID = result.GetCandidates()[0].GetUserId()
	default:
		draft.AssigneeResolution = assigneeAmbiguous
	}
	return draft, nil
}
