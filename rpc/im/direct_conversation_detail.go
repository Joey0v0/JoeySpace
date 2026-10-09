package main

import (
	"context"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const directConversationDetailSQL = "SELECT COALESCE(MAX(id), 0) AS last_message_id FROM messages WHERE chat_type = 1 AND ((from_id = ? AND to_id = ?) OR (from_id = ? AND to_id = ?))"

func (s *imServer) GetMyDirectConversation(ctx context.Context, req *pb.GetMyDirectConversationRequest) (*pb.GetMyDirectConversationResponse, error) {
	userID, err := s.directReader(ctx, req.GetPeerId())
	if err != nil {
		return nil, err
	}
	var row struct{ LastMessageID int64 }
	if err = s.db.WithContext(ctx).Raw(directConversationDetailSQL, userID, req.GetPeerId(), req.GetPeerId(), userID).Scan(&row).Error; err != nil {
		return nil, groupUnreadDBError(ctx)
	}
	if row.LastMessageID == 0 {
		return nil, status.Error(codes.NotFound, "direct conversation not found")
	}
	return &pb.GetMyDirectConversationResponse{Conversation: &pb.DirectConversation{PeerId: req.GetPeerId(), LastMessageId: row.LastMessageID}}, nil
}
