package main

import (
	"context"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *userServer) AuthorizeTeamGroupCreation(ctx context.Context, req *pb.AuthorizeTeamGroupCreationRequest) (*pb.AuthorizeTeamGroupCreationResponse, error) {
	member, err := s.CheckTeamMember(ctx, &pb.CheckTeamMemberRequest{TeamId: req.GetTeamId()})
	if err != nil {
		return nil, err
	}
	if member.GetRole() != 2 {
		return nil, status.Error(codes.PermissionDenied, "team owner required")
	}
	return &pb.AuthorizeTeamGroupCreationResponse{UserId: member.GetUserId(), Generation: member.GetGeneration()}, nil
}
