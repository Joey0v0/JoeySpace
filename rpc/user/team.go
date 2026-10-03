package main

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *userServer) CreateTeam(ctx context.Context, req *pb.CreateTeamRequest) (*pb.CreateTeamResponse, error) {
	if s.db == nil || s.idNode == nil {
		return nil, status.Error(codes.Unavailable, "team database is not enabled")
	}
	name := strings.TrimSpace(req.GetName())
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 {
		return nil, status.Error(codes.InvalidArgument, "invalid team name")
	}

	// 复用本人查询的验签与账户状态校验；请求体不能指定创建者。
	creator, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{})
	if err != nil {
		return nil, err
	}
	teamID := s.idNode.Generate().Int64()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		team := struct {
			ID      int64
			Name    string
			OwnerID int64
		}{teamID, name, creator.GetId()}
		if err := tx.Table("teams").Create(&team).Error; err != nil {
			return err
		}
		member := struct {
			TeamID int64
			UserID int64
			Role   int8
		}{teamID, creator.GetId(), 2}
		return tx.Table("team_members").Create(&member).Error
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		logx.WithContext(ctx).Errorf("create team failed: %v", err)
		return nil, status.Error(codes.Unavailable, "team database unavailable")
	}
	return &pb.CreateTeamResponse{TeamId: teamID}, nil
}
