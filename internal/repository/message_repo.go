package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"gorm.io/gorm"
)

// MessageRepository 消息数据访问接口
type MessageRepository interface {
	Create(ctx context.Context, msg *model.Message) error
	GetHistory(ctx context.Context, userID, targetID int64, chatType int8, cursorMsgID int64, limit int) ([]model.Message, error)
	CreateOffline(ctx context.Context, offline *model.OfflineMessage) error
	ListOffline(ctx context.Context, userID int64) ([]model.Message, error)
	DeleteOffline(ctx context.Context, userID int64, messageIDs []int64) error
}

type messageRepository struct {
	db *gorm.DB
}

// NewMessageRepository 创建消息 Repository
func NewMessageRepository() MessageRepository {
	return &messageRepository{db: DB}
}

func (r *messageRepository) Create(ctx context.Context, msg *model.Message) error {
	if msg == nil {
		return errors.New("message is required")
	}
	// Existing producers omit the new fields; they remain ordinary user messages.
	if msg.SenderType == 0 {
		msg.SenderType = model.MessageSenderUser
	}
	switch msg.SenderType {
	case model.MessageSenderUser:
		if msg.InitiatorID != 0 {
			return errors.New("user message cannot have a bot initiator")
		}
	case model.MessageSenderBot:
		if msg.ChatType != 2 || msg.ToID <= 0 || msg.FromID <= 0 || msg.InitiatorID <= 0 {
			return errors.New("bot message requires a group, bot ID and initiator")
		}
	default:
		return errors.New("unknown message sender type")
	}
	if msg.ContentType == model.MessageContentTaskCard {
		if msg.SenderType != model.MessageSenderBot || msg.ChatType != 2 {
			return errors.New("task card requires a bot group message")
		}
		if _, err := model.DecodeTaskCreatedCard(msg.Content); err != nil {
			return fmt.Errorf("invalid task card: %w", err)
		}
	}
	if err := r.db.WithContext(ctx).Create(msg).Error; err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			var existing model.Message
			if lookupErr := r.db.WithContext(ctx).Where("msg_id = ?", msg.MsgID).Take(&existing).Error; lookupErr == nil {
				if existing.SenderType == 0 {
					existing.SenderType = model.MessageSenderUser
				}
				if existing.FromID != msg.FromID || existing.SenderType != msg.SenderType || existing.InitiatorID != msg.InitiatorID || existing.ToID != msg.ToID || existing.ChatType != msg.ChatType || existing.ContentType != msg.ContentType || existing.Content != msg.Content {
					return fmt.Errorf("message ID %q belongs to different content", msg.MsgID)
				}
				msg.ID = existing.ID
				msg.CreatedAt = existing.CreatedAt
				return nil
			}
		}
		return fmt.Errorf("create message failed: %w", err)
	}
	return nil
}

func (r *messageRepository) GetHistory(ctx context.Context, userID, targetID int64, chatType int8, cursorMsgID int64, limit int) ([]model.Message, error) {
	var messages []model.Message
	query := r.db.WithContext(ctx).Where("chat_type = ?", chatType)

	if chatType == 1 {
		// 单聊：查找两个用户之间的消息
		query = query.Where(
			"(from_id = ? AND to_id = ?) OR (from_id = ? AND to_id = ?)",
			userID, targetID, targetID, userID,
		)
	} else {
		// 群聊：查找发送到该群组的消息
		query = query.Where("to_id = ?", targetID)
	}

	if cursorMsgID > 0 {
		query = query.Where("id < ?", cursorMsgID)
	}

	err := query.Order("id DESC").Limit(limit).Find(&messages).Error
	if err != nil {
		return nil, fmt.Errorf("get message history failed: %w", err)
	}
	return messages, nil
}

func (r *messageRepository) CreateOffline(ctx context.Context, offline *model.OfflineMessage) error {
	if err := r.db.WithContext(ctx).Create(offline).Error; err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			var existing model.OfflineMessage
			if lookupErr := r.db.WithContext(ctx).Select("id").
				Where("user_id = ? AND message_id = ?", offline.UserID, offline.MessageID).
				Take(&existing).Error; lookupErr == nil {
				offline.ID = existing.ID
				return nil
			}
		}
		return fmt.Errorf("create offline message failed: %w", err)
	}
	return nil
}

func (r *messageRepository) ListOffline(ctx context.Context, userID int64) ([]model.Message, error) {
	var messages []model.Message
	err := r.db.WithContext(ctx).
		Table("messages").
		Joins("JOIN offline_messages ON offline_messages.message_id = messages.id").
		Where("offline_messages.user_id = ?", userID).
		Order("messages.id ASC").
		Find(&messages).Error
	if err != nil {
		return nil, fmt.Errorf("list offline messages failed: %w", err)
	}
	return messages, nil
}

func (r *messageRepository) DeleteOffline(ctx context.Context, userID int64, messageIDs []int64) error {
	if len(messageIDs) == 0 {
		return nil
	}
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND message_id IN ?", userID, messageIDs).
		Delete(&model.OfflineMessage{}).Error
	if err != nil {
		return fmt.Errorf("delete offline messages failed: %w", err)
	}
	return nil
}
