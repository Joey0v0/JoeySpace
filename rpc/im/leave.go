package main

import (
	"context"

	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// imLeaveServer is not registered until the dedicated User-only mTLS listener
// is wired. An ordinary IM caller must never be able to close memberships.
type imLeaveServer struct {
	pb.UnimplementedIMLeaveServer
	db          *gorm.DB
	userDNSName string
}

func (s *imLeaveServer) CloseTeamGroupMemberships(ctx context.Context, req *pb.CloseTeamGroupMembershipsRequest) (*pb.CloseTeamGroupMembershipsResponse, error) {
	if ctx == nil || s == nil {
		return nil, status.Error(codes.Unauthenticated, "verified User TLS identity required")
	}
	if err := rpcauth.RequireServiceIdentity(ctx, s.userDNSName); err != nil {
		return nil, err
	}
	closed, err := closeTeamGroupMemberships(ctx, s.db, req.GetTeamId(), req.GetUserId(), req.GetGeneration())
	if err != nil {
		return nil, err
	}
	return &pb.CloseTeamGroupMembershipsResponse{ClosedThroughGeneration: closed}, nil
}
