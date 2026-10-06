package main

import (
	"context"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const triggerTeamTimeout = 3 * time.Second

// This service trusts only the IM transport identity. It checks current User
// membership, without interpreting JWTs or returning user profile information.
func (s *triggerTeamServer) CheckTriggerTeamMember(ctx context.Context, req *pb.CheckTriggerTeamMemberRequest) (*pb.CheckTriggerTeamMemberResponse, error) {
	if ctx == nil {
		return nil, status.Error(codes.Unauthenticated, "verified service TLS identity required")
	}
	name := ""
	if s != nil {
		name = s.imDNSName
	}
	if err := rpcauth.RequireServiceIdentity(ctx, name); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	actorID, teamID := req.GetActorId(), req.GetTeamId()
	if actorID <= 0 || teamID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "positive trigger actor and team are required")
	}
	if s == nil || s.db == nil {
		return nil, status.Error(codes.Unavailable, "trigger team database is not enabled")
	}
	ctx, cancel := context.WithTimeout(ctx, triggerTeamTimeout)
	defer cancel()
	rows, err := s.db.WithContext(ctx).Table("team_members").Select("users.status, team_members.generation").
		Joins("JOIN users ON users.id = team_members.user_id").
		Where("team_members.team_id = ? AND team_members.user_id = ? AND team_members.membership_state = ?", teamID, actorID, model.TeamMembershipActive).Limit(2).Rows()
	if err != nil {
		return nil, triggerTeamStorageError(ctx)
	}
	defer rows.Close()
	found := false
	var userStatus int8
	var generation int64
	for rows.Next() {
		if found {
			return nil, triggerTeamStorageError(ctx)
		}
		if err := rows.Scan(&userStatus, &generation); err != nil || generation <= 0 {
			return nil, triggerTeamStorageError(ctx)
		}
		found = true
	}
	if rows.Err() != nil || ctx.Err() != nil {
		return nil, triggerTeamStorageError(ctx)
	}
	if !found || userStatus != 1 {
		return nil, status.Error(codes.PermissionDenied, "current active team membership required")
	}
	return &pb.CheckTriggerTeamMemberResponse{ActorId: actorID, TeamId: teamID, Generation: generation}, nil
}

func triggerTeamStorageError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return status.Error(codes.Unavailable, "trigger team database unavailable")
}
