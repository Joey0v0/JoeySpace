package main

import (
	"context"
	"errors"
	"strings"

	"github.com/bwmarrin/snowflake"
	jwtv5 "github.com/golang-jwt/jwt/v5"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type imServer struct {
	pb.UnimplementedIMServer
	db         *gorm.DB
	jwtSecret  string
	idNode     *snowflake.Node
	teamClient interface {
		CheckTeamMember(context.Context, *userpb.CheckTeamMemberRequest, ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error)
		AuthorizeTeamGroupCreation(context.Context, *userpb.AuthorizeTeamGroupCreationRequest, ...grpc.CallOption) (*userpb.AuthorizeTeamGroupCreationResponse, error)
	}
}

func (s *imServer) CheckGroupMember(ctx context.Context, req *pb.CheckGroupMemberRequest) (*pb.CheckGroupMemberResponse, error) {
	if s.db == nil || s.jwtSecret == "" {
		return nil, status.Error(codes.Unavailable, "IM membership check is not enabled")
	}
	if req.GetGroupId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid group ID")
	}
	claimsUserID, authorization, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}

	var member struct{ TeamID *int64 }
	err = s.db.WithContext(ctx).Table("group_members").Select("groups.team_id").
		Joins("JOIN groups ON groups.id = group_members.group_id").
		Where("group_members.group_id = ? AND group_members.user_id = ?", req.GetGroupId(), claimsUserID).Take(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.PermissionDenied, "group membership required")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		logx.WithContext(ctx).Errorf("check group membership failed: %v", err)
		return nil, status.Error(codes.Unavailable, "IM database unavailable")
	}
	if member.TeamID != nil {
		if s.teamClient == nil {
			return nil, status.Error(codes.Unavailable, "team membership check is not enabled")
		}
		teamCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", authorization))
		if _, err := s.teamClient.CheckTeamMember(teamCtx, &userpb.CheckTeamMemberRequest{TeamId: *member.TeamID}); err != nil {
			switch status.Code(err) {
			case codes.PermissionDenied, codes.Unauthenticated, codes.DeadlineExceeded, codes.Canceled:
				return nil, err
			default:
				return nil, status.Error(codes.Unavailable, "team membership check unavailable")
			}
		}
	}
	return &pb.CheckGroupMemberResponse{}, nil
}

func (s *imServer) authenticatedUser(ctx context.Context) (int64, string, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	headers := md.Get("authorization")
	if len(headers) != 1 {
		return 0, "", status.Error(codes.Unauthenticated, "invalid login token")
	}
	parts := strings.Fields(headers[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return 0, "", status.Error(codes.Unauthenticated, "invalid login token")
	}
	claims := &pkgjwt.Claims{}
	token, err := jwtv5.ParseWithClaims(parts[1], claims, func(*jwtv5.Token) (interface{}, error) {
		return []byte(s.jwtSecret), nil
	}, jwtv5.WithValidMethods([]string{"HS256"}), jwtv5.WithExpirationRequired(), jwtv5.WithIssuer("go-im"))
	if err != nil || !token.Valid || claims.UserID <= 0 {
		return 0, "", status.Error(codes.Unauthenticated, "invalid or expired login token")
	}
	return claims.UserID, headers[0], nil
}
