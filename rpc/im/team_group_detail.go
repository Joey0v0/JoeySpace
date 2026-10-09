package main

import (
	"context"
	"errors"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *imServer) directoryTeamGeneration(ctx context.Context, authorization string, teamID, userID int64) (int64, error) {
	membership, err := s.teamClient.CheckTeamMember(metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", authorization)), &userpb.CheckTeamMemberRequest{TeamId: teamID})
	if err != nil {
		switch status.Code(err) {
		case codes.PermissionDenied, codes.Unauthenticated, codes.DeadlineExceeded, codes.Canceled:
			return 0, err
		default:
			return 0, status.Error(codes.Unavailable, "team membership check unavailable")
		}
	}
	if err := validateTeamGroupAuthorization(userID, membership.GetUserId(), membership.GetGeneration()); err != nil {
		return 0, err
	}
	return membership.GetGeneration(), nil
}
func (s *imServer) directoryJoined(ctx context.Context, groupID, teamID, userID, generation int64) (bool, error) {
	err := checkTeamGroupReadGeneration(ctx, s.db, groupID, teamID, userID, generation)
	if status.Code(err) == codes.PermissionDenied {
		return false, nil
	}
	return err == nil, err
}
func (s *imServer) directoryTeamUnchanged(ctx context.Context, authorization string, teamID, userID, generation int64) error {
	current, err := s.directoryTeamGeneration(ctx, authorization, teamID, userID)
	if err != nil {
		return err
	}
	if current != generation {
		return status.Error(codes.PermissionDenied, "team membership changed")
	}
	return nil
}
func (s *imServer) GetTeamGroup(ctx context.Context, req *pb.GetTeamGroupRequest) (*pb.GetTeamGroupResponse, error) {
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group discovery is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetGroupId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team or group ID")
	}
	userID, authorization, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	generation, err := s.directoryTeamGeneration(ctx, authorization, req.GetTeamId(), userID)
	if err != nil {
		return nil, err
	}
	var row struct {
		ID      int64
		Name    string
		OwnerID int64
	}
	err = s.db.WithContext(ctx).Table("groups").Select("id, name, owner_id").Where("id = ? AND team_id = ?", req.GetGroupId(), req.GetTeamId()).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "team group not found")
	}
	if err != nil {
		return nil, teamGroupDBError(ctx, err)
	}
	joined, err := s.directoryJoined(ctx, row.ID, req.GetTeamId(), userID, generation)
	if err != nil {
		return nil, err
	}
	if err = s.directoryTeamUnchanged(ctx, authorization, req.GetTeamId(), userID, generation); err != nil {
		return nil, err
	}
	return &pb.GetTeamGroupResponse{Group: &pb.TeamGroup{GroupId: row.ID, Name: row.Name, OwnerId: row.OwnerID, Joined: joined}}, nil
}
