package agent

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/model"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func validTriggerMemberResponse(response *impb.ResolveTaskTriggerMemberResponse, messageID int64, name string) bool {
	if messageID <= 0 || !model.ValidAgentTriggerMemberName(name) || response == nil || response.GetMessageId() != messageID || response.GetName() != name ||
		response.GetActorId() <= 0 || response.GetTeamId() <= 0 || response.GetGroupId() <= 0 ||
		response.GetRequestKey() != "agent-trigger:tasks:"+strconv.FormatInt(messageID, 10) ||
		proto.Size(response) > model.AgentTriggerMemberResponseLimit || len(response.GetCandidates()) > model.AgentTriggerMemberCandidateLimit ||
		response.GetTruncated() && len(response.GetCandidates()) != model.AgentTriggerMemberCandidateLimit {
		return false
	}
	var previousID int64
	for _, candidate := range response.GetCandidates() {
		if candidate == nil || candidate.GetUserId() <= previousID ||
			!model.ValidAgentTriggerMemberCandidate(candidate.GetUserId(), candidate.GetUsername(), candidate.GetNickname(), name) {
			return false
		}
		previousID = candidate.GetUserId()
	}
	return true
}

// ResolveMember uses only the saved source ID and an exact literal name. It
// shares the dedicated mTLS connection and never sends a caller's identity.
func (c *TriggerContextClient) ResolveMember(ctx context.Context, messageID int64, name string) (*impb.ResolveTaskTriggerMemberResponse, error) {
	if ctx == nil {
		return nil, status.Error(codes.InvalidArgument, "trigger member request context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if messageID <= 0 || !model.ValidAgentTriggerMemberName(name) {
		return nil, status.Error(codes.InvalidArgument, "invalid trigger member request")
	}
	if c == nil || c.rpc == nil || c.conn != nil && c.conn.GetState() == connectivity.Shutdown {
		return nil, status.Error(codes.Unavailable, "trigger member service unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	callCtx = metadata.NewOutgoingContext(callCtx, metadata.MD{})
	response, err := c.rpc.ResolveTaskTriggerMember(callCtx, &impb.ResolveTaskTriggerMemberRequest{MessageId: messageID, Name: name},
		grpc.MaxCallRecvMsgSize(model.AgentTriggerMemberResponseLimit))
	if contextErr := callCtx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if c.conn != nil && c.conn.GetState() == connectivity.Shutdown {
		return nil, status.Error(codes.Unavailable, "trigger member service unavailable")
	}
	if err != nil {
		return nil, safeTriggerContextError(err)
	}
	valid := validTriggerMemberResponse(response, messageID, name)
	if contextErr := callCtx.Err(); contextErr != nil {
		return nil, status.FromContextError(contextErr).Err()
	}
	if !valid {
		return nil, status.Error(codes.Unavailable, "invalid trigger member response")
	}
	return response, nil
}

// resolveAssignee interprets the model's literal only after saved source and
// authorized text validation. A match is a candidate for later person review.
func (c *TriggerContextClient) resolveAssignee(ctx context.Context, source *impb.ReadTaskTriggerContextResponse, draft taskDraft) (taskDraft, error) {
	if ctx == nil {
		return taskDraft{}, status.Error(codes.InvalidArgument, "trigger assignee request context required")
	}
	if err := ctx.Err(); err != nil {
		return taskDraft{}, status.FromContextError(err).Err()
	}
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	validSource := validTriggerContext(source, source.GetMessageId())
	if err := readCtx.Err(); err != nil {
		return taskDraft{}, status.FromContextError(err).Err()
	}
	if !validSource {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "invalid saved trigger context")
	}
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
	if !assigneeMentionInAuthorizedText(source.GetInstruction(), source.GetMessages(), draft.AssigneeName) {
		return taskDraft{}, status.Error(codes.FailedPrecondition, "assignee mention could not be verified")
	}
	messageID, actorID, teamID, groupID, requestKey := source.GetMessageId(), source.GetActorId(), source.GetTeamId(), source.GetGroupId(), source.GetRequestKey()
	response, err := c.ResolveMember(readCtx, messageID, draft.AssigneeName)
	if err != nil {
		return taskDraft{}, err
	}
	if err := readCtx.Err(); err != nil {
		return taskDraft{}, status.FromContextError(err).Err()
	}
	if response.GetMessageId() != messageID || response.GetActorId() != actorID || response.GetTeamId() != teamID || response.GetGroupId() != groupID || response.GetRequestKey() != requestKey {
		return taskDraft{}, status.Error(codes.Unavailable, "trigger member scope does not match saved source")
	}
	switch {
	case response.GetTruncated():
		draft.AssigneeResolution = assigneeTruncated
	case len(response.GetCandidates()) == 0:
		draft.AssigneeResolution = assigneeNotFound
	case len(response.GetCandidates()) == 1:
		draft.AssigneeResolution = assigneeMatched
		draft.AssigneeID = response.GetCandidates()[0].GetUserId()
	default:
		draft.AssigneeResolution = assigneeAmbiguous
	}
	return draft, nil
}
