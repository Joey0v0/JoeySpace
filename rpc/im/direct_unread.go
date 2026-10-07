package main

import (
	"context"
	"sort"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *imServer) directReader(ctx context.Context, peerID int64) (int64, error) {
	if s.db == nil || s.jwtSecret == "" {
		return 0, status.Error(codes.Unavailable, "direct messages are not enabled")
	}
	if peerID <= 0 {
		return 0, status.Error(codes.InvalidArgument, "invalid peer ID")
	}
	userID, _, err := s.authenticatedUser(ctx)
	if err != nil {
		return 0, err
	}
	if userID == peerID {
		return 0, status.Error(codes.InvalidArgument, "peer must differ from user")
	}
	return userID, nil
}

func (s *imServer) ListDirectMessages(ctx context.Context, req *pb.ListDirectMessagesRequest) (*pb.ListDirectMessagesResponse, error) {
	if req.GetBeforeMessageId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid direct history parameters")
	}
	userID, err := s.directReader(ctx, req.GetPeerId())
	if err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}
	var rows []struct {
		ID          int64
		MsgID       string
		FromID      int64
		ToID        int64
		ContentType int8
		Content     string
		CreatedAt   time.Time
	}
	query := s.db.WithContext(ctx).Table("messages").Select("id, msg_id, from_id, to_id, content_type, content, created_at").
		Where("chat_type = ? AND ((from_id = ? AND to_id = ?) OR (from_id = ? AND to_id = ?))", 1, userID, req.GetPeerId(), req.GetPeerId(), userID)
	if req.GetBeforeMessageId() > 0 {
		query = query.Where("id < ?", req.GetBeforeMessageId())
	}
	if err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, groupUnreadDBError(ctx)
	}
	result := &pb.ListDirectMessagesResponse{Messages: make([]*pb.DirectMessage, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		result.NextBeforeMessageId = rows[len(rows)-1].ID
	}
	for _, row := range rows {
		result.Messages = append(result.Messages, &pb.DirectMessage{
			Id: row.ID, MsgId: row.MsgID, FromId: row.FromID, ToId: row.ToID,
			ContentType: int32(row.ContentType), Content: row.Content, CreatedAtUnixMs: row.CreatedAt.UnixMilli(),
		})
	}
	return result, nil
}

func (s *imServer) GetDirectUnread(ctx context.Context, req *pb.GetDirectUnreadRequest) (*pb.GetDirectUnreadResponse, error) {
	userID, err := s.directReader(ctx, req.GetPeerId())
	if err != nil {
		return nil, err
	}
	count, err := s.directUnreadCount(ctx, userID, req.GetPeerId())
	if err != nil {
		return nil, err
	}
	return &pb.GetDirectUnreadResponse{PeerId: req.GetPeerId(), UnreadCount: count}, nil
}

func (s *imServer) directUnreadCount(ctx context.Context, userID, peerID int64) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM messages
WHERE messages.chat_type = 1 AND messages.from_id = ? AND messages.to_id = ?
AND NOT EXISTS (SELECT 1 FROM im_direct_message_reads AS r
WHERE r.user_id = ? AND r.peer_id = ? AND r.message_id = messages.id)`, peerID, userID, userID, peerID).Scan(&count).Error
	if err != nil || count < 0 {
		return 0, groupUnreadDBError(ctx)
	}
	return count, nil
}

func (s *imServer) MarkDirectMessagesRead(ctx context.Context, req *pb.MarkDirectMessagesReadRequest) (*pb.MarkDirectMessagesReadResponse, error) {
	if len(req.GetMessageIds()) == 0 || len(req.GetMessageIds()) > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid direct read parameters")
	}
	userID, err := s.directReader(ctx, req.GetPeerId())
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(req.GetMessageIds()))
	seen := make(map[int64]struct{}, len(req.GetMessageIds()))
	for _, id := range req.GetMessageIds() {
		if id <= 0 {
			return nil, status.Error(codes.InvalidArgument, "invalid message ID")
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var messages []struct{ ID int64 }
		if err := tx.Table("messages").Select("id").
			Where("chat_type = ? AND from_id = ? AND to_id = ? AND id IN ?", 1, req.GetPeerId(), userID, ids).
			Order("id ASC").Clauses(clause.Locking{Strength: "SHARE"}).Find(&messages).Error; err != nil {
			return groupUnreadDBError(ctx)
		}
		if len(messages) != len(ids) {
			return status.Error(codes.NotFound, "direct messages not found")
		}
		reads := make([]model.DirectMessageRead, 0, len(ids))
		for _, id := range ids {
			reads = append(reads, model.DirectMessageRead{UserID: userID, PeerID: req.GetPeerId(), MessageID: id})
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&reads).Error; err != nil {
			return groupUnreadDBError(ctx)
		}
		return nil
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, err
		}
		return nil, groupUnreadDBError(ctx)
	}
	count, err := s.directUnreadCount(ctx, userID, req.GetPeerId())
	if err != nil {
		return nil, err
	}
	return &pb.MarkDirectMessagesReadResponse{PeerId: req.GetPeerId(), MessageIds: ids, UnreadCount: count}, nil
}
