package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *userServer) AuthorizeTeamGroupCreation(ctx context.Context, req *pb.AuthorizeTeamGroupCreationRequest) (*pb.AuthorizeTeamGroupCreationResponse, error) {
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "team database is not enabled")
	}
	if req.GetTeamId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team ID")
	}

	operator, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil {
		return nil, err
	}
	var membership struct{ Role int8 }
	err = s.db.WithContext(ctx).Table("team_members").Select("role").
		Where("team_id = ? AND user_id = ?", req.GetTeamId(), operator.GetId()).Take(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && membership.Role != 2 {
		return nil, status.Error(codes.PermissionDenied, "team owner required")
	}
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	return &pb.AuthorizeTeamGroupCreationResponse{}, nil
}
