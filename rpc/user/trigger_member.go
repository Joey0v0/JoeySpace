package main

import (
	"context"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const resolveTriggerMemberQuery = `SELECT users.id, users.username, users.nickname
FROM team_members JOIN users ON users.id = team_members.user_id
WHERE team_members.team_id = ? AND users.status = 1
AND (CAST(users.username AS BINARY) = CAST(? AS BINARY) OR CAST(users.nickname AS BINARY) = CAST(? AS BINARY))
AND EXISTS (SELECT 1 FROM team_members AS actor_members JOIN users AS actor_user ON actor_user.id = actor_members.user_id
WHERE actor_members.team_id = ? AND actor_members.user_id = ? AND actor_user.status = 1)
ORDER BY users.id ASC LIMIT 21`

// ResolveTriggerTeamMember accepts only the IM service identity, and returns
// literal name matches within the actor's currently accessible team.
func (s *triggerTeamServer) ResolveTriggerTeamMember(ctx context.Context, req *pb.ResolveTriggerTeamMemberRequest) (*pb.ResolveTriggerTeamMemberResponse, error) {
	if ctx == nil {
		return nil, status.Error(codes.Unauthenticated, "verified service TLS identity required")
	}
	dnsName := ""
	if s != nil {
		dnsName = s.imDNSName
	}
	if err := rpcauth.RequireServiceIdentity(ctx, dnsName); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	actorID, teamID, name := req.GetActorId(), req.GetTeamId(), req.GetName()
	if actorID <= 0 || teamID <= 0 || !model.ValidAgentTriggerMemberName(name) {
		return nil, status.Error(codes.InvalidArgument, "valid trigger member scope and name are required")
	}
	if s == nil || s.db == nil {
		return nil, status.Error(codes.Unavailable, "trigger team database is not enabled")
	}
	ctx, cancel := context.WithTimeout(ctx, triggerTeamTimeout)
	defer cancel()
	qualification := &pb.CheckTriggerTeamMemberRequest{ActorId: actorID, TeamId: teamID}
	if _, err := s.CheckTriggerTeamMember(ctx, qualification); err != nil {
		return nil, err
	}
	rows, err := s.db.WithContext(ctx).Raw(resolveTriggerMemberQuery, teamID, name, name, teamID, actorID).Rows()
	if err != nil {
		return nil, triggerTeamStorageError(ctx)
	}
	defer rows.Close()
	candidates := make([]*pb.TriggerTeamMemberCandidate, 0, model.AgentTriggerMemberCandidateLimit+1)
	var previousID int64
	for rows.Next() {
		var id int64
		var username, nickname string
		if len(candidates) >= model.AgentTriggerMemberCandidateLimit+1 || rows.Scan(&id, &username, &nickname) != nil ||
			id <= previousID || !model.ValidAgentTriggerMemberCandidate(id, username, nickname, name) {
			return nil, triggerTeamStorageError(ctx)
		}
		previousID = id
		candidates = append(candidates, &pb.TriggerTeamMemberCandidate{UserId: id, Username: username, Nickname: nickname})
	}
	if rows.Err() != nil || rows.Close() != nil || ctx.Err() != nil {
		return nil, triggerTeamStorageError(ctx)
	}
	// Empty results also require a fresh check: revocation is never not_found.
	if _, err := s.CheckTriggerTeamMember(ctx, qualification); err != nil {
		return nil, err
	}
	truncated := len(candidates) > model.AgentTriggerMemberCandidateLimit
	if truncated {
		candidates = candidates[:model.AgentTriggerMemberCandidateLimit]
	}
	response := &pb.ResolveTriggerTeamMemberResponse{ActorId: actorID, TeamId: teamID, Name: name, Candidates: candidates, Truncated: truncated}
	if ctx.Err() != nil {
		return nil, triggerTeamStorageError(ctx)
	}
	if proto.Size(response) > model.AgentTriggerMemberResponseLimit {
		return nil, triggerTeamStorageError(ctx)
	}
	return response, nil
}
