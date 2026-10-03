package main

import (
	"context"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ListOfflineMessages 只读返回当前用户尚未确认的消息；删除由独立 ACK 负责。
func (s *imServer) ListOfflineMessages(ctx context.Context, _ *pb.ListOfflineMessagesRequest) (*pb.ListOfflineMessagesResponse, error) {
	if s.db == nil || s.jwtSecret == "" {
		return nil, status.Error(codes.Unavailable, "offline messages are not enabled")
	}
	userID, _, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	var messages []model.Message
	err = s.db.WithContext(ctx).Table("messages").
		Select("messages.id, messages.msg_id, messages.from_id, messages.sender_type, messages.initiator_id, messages.to_id, messages.chat_type, messages.content_type, messages.content, messages.created_at").
		Joins("JOIN offline_messages ON offline_messages.message_id = messages.id").
		Where("offline_messages.user_id = ?", userID).
		Order("messages.id ASC").Find(&messages).Error
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		logx.WithContext(ctx).Errorf("list offline messages failed: %v", err)
		return nil, status.Error(codes.Unavailable, "IM database unavailable")
	}
	result := &pb.ListOfflineMessagesResponse{Messages: make([]*pb.OfflineMessage, 0, len(messages))}
	for _, message := range messages {
		if message.SenderType == 0 {
			message.SenderType = model.MessageSenderUser
		}
		result.Messages = append(result.Messages, &pb.OfflineMessage{
			Id: message.ID, MsgId: message.MsgID, FromId: message.FromID, ToId: message.ToID,
			SenderType: int32(message.SenderType), InitiatorId: message.InitiatorID,
			ChatType: int32(message.ChatType), ContentType: int32(message.ContentType),
			Content: message.Content, CreatedAt: timestamppb.New(message.CreatedAt),
		})
	}
	return result, nil
}

// AckOfflineMessages 只删除 Token 所属用户明确确认的离线记录，重复确认也是成功。
func (s *imServer) AckOfflineMessages(ctx context.Context, req *pb.AckOfflineMessagesRequest) (*pb.AckOfflineMessagesResponse, error) {
	if s.db == nil || s.jwtSecret == "" {
		return nil, status.Error(codes.Unavailable, "offline acknowledgement is not enabled")
	}
	messageIDs := req.GetMessageIds()
	if len(messageIDs) == 0 || len(messageIDs) > 1000 {
		return nil, status.Error(codes.InvalidArgument, "invalid message IDs")
	}
	for _, messageID := range messageIDs {
		if messageID <= 0 {
			return nil, status.Error(codes.InvalidArgument, "invalid message IDs")
		}
	}
	userID, _, err := s.authenticatedUser(ctx)
	if err != nil {
		return nil, err
	}
	err = s.db.WithContext(ctx).
		Where("user_id = ? AND message_id IN ?", userID, messageIDs).
		Delete(&model.OfflineMessage{}).Error
	if err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		logx.WithContext(ctx).Errorf("ack offline messages failed: %v", err)
		return nil, status.Error(codes.Unavailable, "IM database unavailable")
	}
	return &pb.AckOfflineMessagesResponse{}, nil
}
