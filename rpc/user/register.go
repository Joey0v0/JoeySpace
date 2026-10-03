package main

import (
	"context"
	"errors"
	"unicode/utf8"

	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/user/pb"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func (s *userServer) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	if s.db == nil || s.idNode == nil {
		return nil, status.Error(codes.Unavailable, "user database is not enabled")
	}
	username, password, nickname := req.GetUsername(), req.GetPassword(), req.GetNickname()
	if n := utf8.RuneCountInString(username); n < 3 || n > 32 {
		return nil, status.Error(codes.InvalidArgument, "invalid username")
	}
	if n := utf8.RuneCountInString(password); n < 6 || n > 64 || len(password) > 72 {
		return nil, status.Error(codes.InvalidArgument, "invalid password")
	}
	if utf8.RuneCountInString(nickname) > 64 {
		return nil, status.Error(codes.InvalidArgument, "invalid nickname")
	}
	if nickname == "" {
		nickname = username
	}

	var existing struct{ ID int64 }
	err := s.db.WithContext(ctx).Table("users").Select("id").Where("username = ?", username).Take(&existing).Error
	if err == nil {
		return nil, status.Error(codes.AlreadyExists, "username already exists")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, status.Error(codes.Unavailable, "user database unavailable")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, status.Error(codes.Internal, "cannot hash password")
	}
	user := struct {
		ID       int64
		Username string
		Password string
		Nickname string
		Status   int8
	}{s.idNode.Generate().Int64(), username, string(hash), nickname, 1}
	if err := s.db.WithContext(ctx).Table("users").Create(&user).Error; err != nil {
		var mysqlErr *driver.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return nil, status.Error(codes.AlreadyExists, "username already exists")
		}
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		return nil, status.Error(codes.Unavailable, "user database unavailable")
	}
	return &pb.RegisterResponse{}, nil
}
