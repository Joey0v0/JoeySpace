package main

import (
	"context"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const directConversationSnapshotSQL = "SELECT COALESCE(MAX(id), 0) AS upper_id FROM messages WHERE chat_type = 1 AND ((from_id = ? AND to_id > 0 AND to_id <> ?) OR (to_id = ? AND from_id > 0 AND from_id <> ?))"

// Filter each direction before grouping. The cursor applies to each peer's
// maximum inside the fixed upper bound, never to the individual message rows.
const directConversationListSQL = `SELECT peer_id, MAX(id) AS last_message_id FROM (
SELECT to_id AS peer_id, id FROM messages WHERE chat_type = 1 AND from_id = ? AND to_id > 0 AND to_id <> ? AND id <= ?
UNION ALL
SELECT from_id AS peer_id, id FROM messages WHERE chat_type = 1 AND to_id = ? AND from_id > 0 AND from_id <> ? AND id <= ?
) AS direct_messages GROUP BY peer_id HAVING (? = 0 OR MAX(id) < ?) ORDER BY last_message_id DESC LIMIT ?`

func (s *imServer) ListMyDirectConversations(ctx context.Context, req *pb.ListMyDirectConversationsRequest) (*pb.ListMyDirectConversationsResponse, error) {
	if s.db == nil || s.jwtSecret == "" {
		return nil, status.Error(codes.Unavailable, "direct conversation listing is not enabled")
	}
	upper, before := req.GetSnapshotUpperMessageId(), req.GetBeforeLastMessageId()
	if upper < 0 || before < 0 || before > upper || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid direct conversation parameters")
	}
	userID, _, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}
	if upper == 0 {
		var row struct{ UpperID int64 }
		if err = s.db.WithContext(ctx).Raw(directConversationSnapshotSQL, userID, userID, userID, userID).Scan(&row).Error; err != nil {
			return nil, groupUnreadDBError(ctx)
		}
		upper = row.UpperID
	}
	result := &pb.ListMyDirectConversationsResponse{Conversations: make([]*pb.DirectConversation, 0), SnapshotUpperMessageId: upper}
	if upper == 0 {
		return result, nil
	}
	var rows []struct {
		PeerID        int64
		LastMessageID int64
	}
	if err = s.db.WithContext(ctx).Raw(directConversationListSQL, userID, userID, upper, userID, userID, upper, before, before, limit+1).Scan(&rows).Error; err != nil {
		return nil, groupUnreadDBError(ctx)
	}
	if len(rows) > limit {
		rows = rows[:limit]
		result.NextBeforeLastMessageId = rows[len(rows)-1].LastMessageID
	}
	for _, row := range rows {
		result.Conversations = append(result.Conversations, &pb.DirectConversation{PeerId: row.PeerID, LastMessageId: row.LastMessageID})
	}
	return result, nil
}
