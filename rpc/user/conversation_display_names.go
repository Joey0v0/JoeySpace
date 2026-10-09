package main

import (
	"context"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BatchGetConversationDisplayNames is internal enrichment for peers already
// proven by IM. Gateway owns that proof; this RPC exposes only display fields.
func (s *userServer) BatchGetConversationDisplayNames(ctx context.Context, req *pb.BatchGetConversationDisplayNamesRequest) (*pb.BatchGetConversationDisplayNamesResponse, error) {
	if len(req.GetUserIds()) == 0 || len(req.GetUserIds()) > 100 {
		return nil, status.Error(codes.InvalidArgument, "expected 1 to 100 user IDs")
	}
	ids := make([]int64, 0, len(req.GetUserIds()))
	seen := make(map[int64]struct{}, len(req.GetUserIds()))
	for _, id := range req.GetUserIds() {
		if id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "user IDs must be positive")
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if _, err := s.GetMyInfo(ctx, &pb.GetMyInfoRequest{}); err != nil {
		return nil, err
	}
	var rows []struct {
		ID       int64
		Username string
		Nickname string
	}
	err := s.db.WithContext(ctx).Table("users").Select("id, username, nickname").Where("id IN ? AND status = ?", ids, 1).Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, teamMemberDBError(ctx, err)
	}
	result := &pb.BatchGetConversationDisplayNamesResponse{Users: make([]*pb.ConversationDisplayName, 0, len(rows))}
	for _, row := range rows {
		name := row.Nickname
		if name == "" {
			name = row.Username
		}
		result.Users = append(result.Users, &pb.ConversationDisplayName{UserId: row.ID, DisplayName: name})
	}
	return result, nil
}
