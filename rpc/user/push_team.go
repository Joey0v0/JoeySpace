package main

import (
	"context"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const pushClientDNSName = "push.go-im.internal"

type pushTeamServer struct {
	pb.UnimplementedUserPushServer
	db *gorm.DB
}

// CheckPushTeamMember checks User-owned current team eligibility only. Push
// must separately verify the current IM group membership and closure fence.
func (s *pushTeamServer) CheckPushTeamMember(ctx context.Context, req *pb.CheckPushTeamMemberRequest) (*pb.CheckPushTeamMemberResponse, error) {
	if ctx == nil {
		return nil, status.Error(codes.Unauthenticated, "verified Push TLS identity required")
	}
	if err := rpcauth.RequireServiceIdentity(ctx, pushClientDNSName); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	teamID, userID := req.GetTeamId(), req.GetUserId()
	if teamID <= 0 || userID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "positive team and user IDs required")
	}
	if s == nil || s.db == nil {
		return nil, status.Error(codes.Unavailable, "push team database unavailable")
	}

	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := s.db.WithContext(queryCtx).Table("team_members").Select("users.status, team_members.generation").
		Joins("JOIN users ON users.id = team_members.user_id").
		Where("team_members.team_id = ? AND team_members.user_id = ? AND team_members.membership_state = ?", teamID, userID, model.TeamMembershipActive).
		Limit(2).Rows()
	if err != nil {
		return nil, pushTeamStorageError(queryCtx)
	}
	defer rows.Close()
	found := false
	var accountStatus int8
	var generation int64
	for rows.Next() {
		if found {
			return nil, pushTeamStorageError(queryCtx)
		}
		if err := rows.Scan(&accountStatus, &generation); err != nil || generation <= 0 {
			return nil, pushTeamStorageError(queryCtx)
		}
		found = true
	}
	if rows.Err() != nil || queryCtx.Err() != nil {
		return nil, pushTeamStorageError(queryCtx)
	}
	if !found || accountStatus != 1 {
		return nil, status.Error(codes.PermissionDenied, "current active team membership required")
	}
	return &pb.CheckPushTeamMemberResponse{TeamId: teamID, UserId: userID, Generation: generation}, nil
}

func pushTeamStorageError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return status.Error(codes.Unavailable, "push team database unavailable")
}
