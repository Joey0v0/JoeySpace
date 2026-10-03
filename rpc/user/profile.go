package main

import (
	"context"
	"errors"
	"strings"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *userServer) GetMyInfo(ctx context.Context, _ *pb.GetMyInfoRequest) (*pb.GetUserInfoResponse, error) {
	if s.db == nil || s.jwtSecret == "" {
		return nil, status.Error(codes.Unavailable, "profile query is not enabled")
	}
	// 不信任调用方给出的用户 ID；即使直接调用 RPC，也必须验证登录 Token。
	md, _ := metadata.FromIncomingContext(ctx)
	headers := md.Get("authorization")
	if len(headers) != 1 {
		return nil, status.Error(codes.Unauthenticated, "invalid login token")
	}
	parts := strings.Fields(headers[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, status.Error(codes.Unauthenticated, "invalid login token")
	}
	claims := &pkgjwt.Claims{}
	token, err := jwtv5.ParseWithClaims(parts[1], claims, func(*jwtv5.Token) (interface{}, error) {
		return []byte(s.jwtSecret), nil
	}, jwtv5.WithValidMethods([]string{"HS256"}), jwtv5.WithExpirationRequired(), jwtv5.WithIssuer("go-im"))
	if err != nil || !token.Valid || claims.UserID <= 0 {
		return nil, status.Error(codes.Unauthenticated, "invalid or expired login token")
	}

	// 迁移期间只读现有 users 表；不导入旧 Repository，也不读取密码列。
	var user struct {
		ID       int64
		Username string
		Nickname string
		Status   int8
	}
	err = s.db.WithContext(ctx).Table("users").Select("id, username, nickname, status").Where("id = ?", claims.UserID).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "user not found")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		logx.WithContext(ctx).Errorf("query profile failed: %v", err)
		return nil, status.Error(codes.Unavailable, "user database unavailable")
	}
	if user.Status != 1 {
		return nil, status.Error(codes.PermissionDenied, "user is disabled")
	}
	return &pb.GetUserInfoResponse{Id: user.ID, Username: user.Username, Nickname: user.Nickname}, nil
}
