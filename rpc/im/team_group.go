package main

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *imServer) CreateTeamGroup(ctx context.Context, req *pb.CreateTeamGroupRequest) (*pb.CreateTeamGroupResponse, error) {
	if s.db == nil || s.idNode == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group creation is not enabled")
	}
	name := strings.TrimSpace(req.GetName())
	if req.GetTeamId() <= 0 || !utf8.ValidString(name) || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 64 {
		return nil, status.Error(codes.InvalidArgument, "invalid team ID or group name")
	}

	creatorID, authorization, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	md, _ := metadata.FromIncomingContext(ctx)
	keys := md.Get("idempotency-key")
	if len(keys) != 1 || !validRequestKey(keys[0]) {
		return nil, status.Error(codes.InvalidArgument, "invalid idempotency key")
	}
	teamCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", authorization))
	if _, err := s.teamClient.AuthorizeTeamGroupCreation(teamCtx, &userpb.AuthorizeTeamGroupCreationRequest{TeamId: req.GetTeamId()}); err != nil {
		switch status.Code(err) {
		case codes.PermissionDenied, codes.Unauthenticated, codes.DeadlineExceeded, codes.Canceled:
			return nil, err
		default:
			return nil, status.Error(codes.Unavailable, "team authorization unavailable")
		}
	}

	groupID := s.idNode.Generate().Int64()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		group := struct {
			ID         int64
			Name       string
			OwnerID    int64
			TeamID     int64
			RequestKey string
		}{groupID, name, creatorID, req.GetTeamId(), keys[0]}
		if err := tx.Table("groups").Create(&group).Error; err != nil {
			return err
		}
		owner := struct {
			GroupID int64
			UserID  int64
			Role    int8
		}{groupID, creatorID, 2}
		return tx.Table("group_members").Create(&owner).Error
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			var previous struct {
				ID     int64
				TeamID int64
				Name   string
			}
			findErr := s.db.WithContext(ctx).Table("groups").Select("id, team_id, name").
				Where("owner_id = ? AND request_key = ?", creatorID, keys[0]).Take(&previous).Error
			if findErr == nil {
				if previous.TeamID != req.GetTeamId() || previous.Name != name {
					return nil, status.Error(codes.AlreadyExists, "idempotency key used for another group request")
				}
				return &pb.CreateTeamGroupResponse{GroupId: previous.ID}, nil
			}
		}
		logx.WithContext(ctx).Errorf("create team group failed: %v", err)
		return nil, status.Error(codes.Unavailable, "IM database unavailable")
	}
	return &pb.CreateTeamGroupResponse{GroupId: groupID}, nil
}

func validRequestKey(key string) bool {
	if len(key) < 1 || len(key) > 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
