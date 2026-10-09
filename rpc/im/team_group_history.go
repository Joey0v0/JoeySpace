package main

import (
	"context"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/core/logx"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *imServer) ListTeamGroupMessages(ctx context.Context, req *pb.ListTeamGroupMessagesRequest) (*pb.ListTeamGroupMessagesResponse, error) {
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group history is not enabled")
	}
	if req.GetTeamId() <= 0 || req.GetGroupId() <= 0 || req.GetBeforeMessageId() < 0 || req.GetLimit() < 0 || req.GetLimit() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid team group history parameters")
	}
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = 20
	}
	if _, err := s.CheckTeamGroupAccess(ctx, &pb.CheckTeamGroupAccessRequest{TeamId: req.GetTeamId(), GroupId: req.GetGroupId()}); err != nil {
		return nil, err
	}
	var rows []struct {
		ID          int64
		MsgID       string
		FromID      int64
		SenderType  int8
		InitiatorID int64
		ContentType int8
		Content     string
		CreatedAt   time.Time
	}
	query := s.db.WithContext(ctx).Table("messages").Select("id, msg_id, from_id, sender_type, initiator_id, content_type, content, created_at").
		Where("to_id = ? AND chat_type = ?", req.GetGroupId(), 2)
	if req.GetBeforeMessageId() > 0 {
		query = query.Where("id < ?", req.GetBeforeMessageId())
	}
	err := query.Order("id DESC").Limit(limit + 1).Find(&rows).Error
	if err != nil {
		return nil, teamGroupDBError(ctx, err)
	}
	result := &pb.ListTeamGroupMessagesResponse{Messages: make([]*pb.TeamGroupMessage, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		result.NextBeforeMessageId = rows[len(rows)-1].ID
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	mentions, err := loadGroupMentionIDs(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.SenderType == 0 {
			row.SenderType = model.MessageSenderUser
		}
		result.Messages = append(result.Messages, &pb.TeamGroupMessage{
			Id: row.ID, MsgId: row.MsgID, FromId: row.FromID,
			SenderType: int32(row.SenderType), InitiatorId: row.InitiatorID,
			ContentType: int32(row.ContentType), Content: row.Content, CreatedAtUnixMs: row.CreatedAt.UnixMilli(),
			MentionedUserIds: mentions[row.ID],
		})
	}
	return result, nil
}

func teamGroupDBError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	logx.WithContext(ctx).Errorf("team group database query failed: %v", err)
	return status.Error(codes.Unavailable, "IM database unavailable")
}
