package main

import (
	"context"

	"github.com/bwmarrin/snowflake"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type userServer struct {
	pb.UnimplementedUserServer
	db        *gorm.DB
	jwtSecret string
	idNode    *snowflake.Node
	leaveIM   teamLeaveIMCloser
}

func (s *userServer) GetUserInfo(ctx context.Context, req *pb.GetUserInfoRequest) (*pb.GetUserInfoResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if req.GetUserId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "user_id must be positive")
	}
	if req.GetUserId() != 1 {
		return nil, status.Error(codes.NotFound, "demo user not found; only user_id=1 is available")
	}

	// 先验证跨进程调用；后续步骤再用真实数据库查询替换这里。
	return &pb.GetUserInfoResponse{
		Id:       1,
		Username: "demo_user",
		Nickname: "演示用户（非真实数据）",
	}, nil
}
