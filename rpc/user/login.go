package main

import (
	"context"
	"errors"

	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/rpc/user/pb"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

const loginTokenExpireHours = 24

func (s *userServer) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	if s.db == nil || s.jwtSecret == "" {
		return nil, status.Error(codes.Unavailable, "user database is not enabled")
	}
	if req.GetUsername() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "username and password are required")
	}

	var user struct {
		ID       int64
		Password string
		Status   int8
	}
	err := s.db.WithContext(ctx).Table("users").Select("id, password, status").Where("username = ?", req.GetUsername()).Take(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.Unauthenticated, "invalid username or password")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, status.Error(codes.Unavailable, "user database unavailable")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.GetPassword())) != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid username or password")
	}
	if user.Status != 1 {
		return nil, status.Error(codes.PermissionDenied, "user is disabled")
	}
	// 沿用旧服务的 HS256、issuer、user_id 和过期时间格式。
	token, err := pkgjwt.GenerateToken(user.ID, s.jwtSecret, loginTokenExpireHours)
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot issue login token")
	}
	return &pb.LoginResponse{Token: token}, nil
}
