package main

import (
	"context"
	"errors"

	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *imServer) CheckTeamGroupMessage(ctx context.Context, req *pb.CheckTeamGroupMessageRequest) (*pb.CheckTeamGroupMessageResponse, error) {
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group message check is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetGroupId() <= 0 || req.GetMessageId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team group message parameters")
	}
	if _, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: req.GetGroupId()}); err != nil {
		return nil, err
	}

	var message struct{ ID int64 }
	err := s.db.WithContext(ctx).Table("messages").Select("messages.id").
		Joins("JOIN groups ON groups.id = messages.to_id").
		Where("messages.id = ? AND messages.to_id = ? AND messages.chat_type = ? AND groups.team_id = ?",
			req.GetMessageId(), req.GetGroupId(), 2, req.GetTeamId()).Take(&message).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "team group message not found")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		logx.WithContext(ctx).Errorf("check team group message failed: %v", err)
		return nil, status.Error(codes.Unavailable, "IM database unavailable")
	}
	return &pb.CheckTeamGroupMessageResponse{}, nil
}
