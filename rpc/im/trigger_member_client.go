package main

import (
	"context"
	"errors"
	"time"

	"github.com/yjydist/go-im/internal/model"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func validTriggerMemberResponse(response *userpb.ResolveTriggerTeamMemberResponse, actorID, teamID int64, name string) bool {
	if response == nil || actorID <= 0 || teamID <= 0 || !model.ValidAgentTriggerMemberName(name) ||
		response.GetActorId() != actorID || response.GetTeamId() != teamID || response.GetName() != name ||
		len(response.Candidates) > model.AgentTriggerMemberCandidateLimit ||
		response.Truncated && len(response.Candidates) != model.AgentTriggerMemberCandidateLimit ||
		proto.Size(response) > model.AgentTriggerMemberResponseLimit {
		return false
	}
	var previous int64
	for _, candidate := range response.Candidates {
		if candidate == nil || candidate.UserId <= previous ||
			!model.ValidAgentTriggerMemberCandidate(candidate.UserId, candidate.Username, candidate.Nickname, name) {
			return false
		}
		previous = candidate.UserId
	}
	return true
}

func triggerMemberCallError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	code := status.Code(err)
	if errors.Is(err, context.Canceled) {
		code = codes.Canceled
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = codes.DeadlineExceeded
	}
	switch code {
	case codes.PermissionDenied, codes.Unauthenticated, codes.InvalidArgument, codes.Canceled, codes.DeadlineExceeded:
	default:
		code = codes.Unavailable
	}
	return status.Error(code, "trigger member resolution unavailable")
}

// Resolve uses the existing service connection, never a user token or a second
// connection. Its per-call limit leaves the old Check response limit unchanged.
func (c *triggerTeamClient) Resolve(ctx context.Context, actorID, teamID int64, name string) (*userpb.ResolveTriggerTeamMemberResponse, error) {
	if ctx == nil {
		return nil, status.Error(codes.InvalidArgument, "trigger member context required")
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	if actorID <= 0 || teamID <= 0 || !model.ValidAgentTriggerMemberName(name) {
		return nil, status.Error(codes.InvalidArgument, "valid trigger member scope and literal name required")
	}
	if c == nil || c.rpc == nil || c.conn != nil && c.conn.GetState() == connectivity.Shutdown {
		return nil, status.Error(codes.Unavailable, "trigger member resolution unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	callCtx = metadata.NewOutgoingContext(callCtx, metadata.MD{})
	response, err := c.rpc.ResolveTriggerTeamMember(callCtx, &userpb.ResolveTriggerTeamMemberRequest{ActorId: actorID, TeamId: teamID, Name: name},
		grpc.MaxCallRecvMsgSize(model.AgentTriggerMemberResponseLimit))
	if callCtx.Err() != nil {
		return nil, status.FromContextError(callCtx.Err()).Err()
	}
	if c.conn != nil && c.conn.GetState() == connectivity.Shutdown {
		return nil, status.Error(codes.Unavailable, "trigger member resolution unavailable")
	}
	if err != nil {
		return nil, triggerMemberCallError(callCtx, err)
	}
	valid := validTriggerMemberResponse(response, actorID, teamID, name)
	if callCtx.Err() != nil {
		return nil, status.FromContextError(callCtx.Err()).Err()
	}
	if !valid {
		return nil, status.Error(codes.Unavailable, "invalid trigger member response")
	}
	return response, nil
}
