package main

import (
	"context"
	"sort"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// GetTeamGroupUnread only counts messages. Pulling history or deliveries does
// not create a reading receipt and a message ID is not a commit-order cursor.
func (s *imServer) GetTeamGroupUnread(ctx context.Context, req *pb.GetTeamGroupUnreadRequest) (*pb.GetTeamGroupUnreadResponse, error) {
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group unread is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetGroupId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team or group ID")
	}
	userID, _, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	access := &pb.CheckTeamGroupAccessRequest{TeamId: req.GetTeamId(), GroupId: req.GetGroupId()}
	if _, err := s.CheckTeamGroupAccess(ctx, access); err != nil {
		return nil, err
	}
	count, err := s.teamGroupUnreadCount(ctx, userID, req.GetGroupId())
	if err != nil {
		return nil, err
	}
	if _, err := s.CheckTeamGroupAccess(ctx, access); err != nil {
		return nil, err
	}
	return &pb.GetTeamGroupUnreadResponse{TeamId: req.GetTeamId(), GroupId: req.GetGroupId(), UnreadCount: count}, nil
}

// MarkTeamGroupMessagesRead validates the whole explicitly selected set before
// writing this user's receipts. A retry never changes the first database time.
func (s *imServer) MarkTeamGroupMessagesRead(ctx context.Context, req *pb.MarkTeamGroupMessagesReadRequest) (*pb.MarkTeamGroupMessagesReadResponse, error) {
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group unread is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetGroupId() <= 0 || len(req.GetMessageIds()) == 0 || len(req.GetMessageIds()) > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid team group read parameters")
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
	userID, _, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	access := &pb.CheckTeamGroupAccessRequest{TeamId: req.GetTeamId(), GroupId: req.GetGroupId()}
	if _, err := s.CheckTeamGroupAccess(ctx, access); err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var messages []struct {
			ID         int64
			FromID     int64
			SenderType int8
		}
		if err := tx.Table("messages").Select("id, from_id, sender_type").
			Where("to_id = ? AND chat_type = ? AND id IN ?", req.GetGroupId(), 2, ids).
			Order("id ASC").Clauses(clause.Locking{Strength: "SHARE"}).Find(&messages).Error; err != nil {
			return groupUnreadDBError(ctx)
		}
		if len(messages) != len(ids) {
			return status.Error(codes.NotFound, "team group messages not found")
		}
		reads := make([]model.GroupMessageRead, 0, len(messages))
		for _, message := range messages {
			if message.FromID == userID && (message.SenderType == 0 || message.SenderType == model.MessageSenderUser) {
				continue
			}
			reads = append(reads, model.GroupMessageRead{UserID: userID, GroupID: req.GetGroupId(), MessageID: message.ID})
		}
		if len(reads) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&reads).Error; err != nil {
				return groupUnreadDBError(ctx)
			}
		}
		return nil
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, err
		}
		return nil, groupUnreadDBError(ctx)
	}
	// Revocation or a lost response here may occur after a committed receipt.
	// Counting again includes concurrent arrivals, even ones with a smaller ID.
	count, err := s.GetTeamGroupUnread(ctx, &pb.GetTeamGroupUnreadRequest{TeamId: req.GetTeamId(), GroupId: req.GetGroupId()})
	if err != nil {
		return nil, err
	}
	return &pb.MarkTeamGroupMessagesReadResponse{TeamId: req.GetTeamId(), GroupId: req.GetGroupId(), MessageIds: ids, UnreadCount: count.GetUnreadCount()}, nil
}

func (s *imServer) teamGroupUnreadCount(ctx context.Context, userID, groupID int64) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM messages
WHERE messages.to_id = ? AND messages.chat_type = 2
AND NOT (messages.sender_type IN (0, 1) AND messages.from_id = ?)
AND NOT EXISTS (SELECT 1 FROM im_group_message_reads AS r
WHERE r.user_id = ? AND r.group_id = ? AND r.message_id = messages.id)`, groupID, userID, userID, groupID).Scan(&count).Error
	if err != nil || count < 0 {
		return 0, groupUnreadDBError(ctx)
	}
	return count, nil
}

// Never attach driver errors, message content or credentials to new logs or
// responses. Preserve cancellation while otherwise using the existing DB code.
func groupUnreadDBError(ctx context.Context) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	return status.Error(codes.Unavailable, "IM database unavailable")
}
