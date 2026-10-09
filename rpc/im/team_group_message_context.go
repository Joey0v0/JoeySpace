package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type messageContextRow struct {
	ID          int64
	MsgID       string
	FromID      int64
	SenderType  int8
	InitiatorID int64
	ContentType int8
	Content     string
	CreatedAt   time.Time
}

type messageContextAccess struct{ userID, generation int64 }

// The ordinary access RPC returns no generation. This private snapshot follows
// the same authorization/fence checks and additionally lets this read reject a
// leave/rejoin that changes generation while both checks individually succeed.
func (s *imServer) messageContextAccessSnapshot(ctx context.Context, teamID, groupID int64) (messageContextAccess, error) {
	userID, authorization, err := s.authenticatedUser(ctx)
	if err != nil {
		return messageContextAccess{}, err
	}
	var member struct{ TeamID *int64 }
	err = s.db.WithContext(ctx).Table("group_members").Select("`groups`.team_id").Joins("JOIN `groups` ON `groups`.id = group_members.group_id").Where("group_members.group_id = ? AND group_members.user_id = ?", groupID, userID).Take(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return messageContextAccess{}, status.Error(codes.PermissionDenied, "group membership required")
	}
	if err != nil {
		return messageContextAccess{}, teamGroupDBError(ctx, err)
	}
	if member.TeamID != nil {
		teamCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+strings.Fields(authorization)[1]))
		membership, err := s.teamClient.CheckTeamMember(teamCtx, &userpb.CheckTeamMemberRequest{TeamId: *member.TeamID})
		if err != nil {
			switch status.Code(err) {
			case codes.PermissionDenied, codes.Unauthenticated, codes.DeadlineExceeded, codes.Canceled:
				return messageContextAccess{}, err
			default:
				return messageContextAccess{}, status.Error(codes.Unavailable, "team membership check unavailable")
			}
		}
		if err := validateTeamGroupAuthorization(userID, membership.GetUserId(), membership.GetGeneration()); err != nil {
			return messageContextAccess{}, err
		}
		if err := checkTeamGroupReadGeneration(ctx, s.db, groupID, *member.TeamID, userID, membership.GetGeneration()); err != nil {
			return messageContextAccess{}, err
		}
		var group struct{ TeamID *int64 }
		err = s.db.WithContext(ctx).Table("groups").Select("team_id").Where("id = ?", groupID).Take(&group).Error
		if errors.Is(err, gorm.ErrRecordNotFound) || err == nil && (group.TeamID == nil || *group.TeamID != teamID || *member.TeamID != teamID) {
			return messageContextAccess{}, status.Error(codes.NotFound, "team group not found")
		}
		if err != nil {
			return messageContextAccess{}, teamGroupDBError(ctx, err)
		}
		return messageContextAccess{userID: userID, generation: membership.GetGeneration()}, nil
	}
	return messageContextAccess{}, status.Error(codes.NotFound, "team group not found")
}

func (s *imServer) GetTeamGroupMessageContext(ctx context.Context, req *pb.GetTeamGroupMessageContextRequest) (*pb.GetTeamGroupMessageContextResponse, error) {
	if req.GetTeamId() <= 0 || req.GetGroupId() <= 0 || req.GetMessageId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid message context parameters")
	}
	if s.db == nil || s.jwtSecret == "" || s.teamClient == nil {
		return nil, status.Error(codes.Unavailable, "team group context is not enabled")
	}
	access, err := s.messageContextAccessSnapshot(ctx, req.TeamId, req.GroupId)
	if err != nil {
		return nil, err
	}
	query := func() *gorm.DB {
		return s.db.WithContext(ctx).Table("messages").Select("id, msg_id, from_id, sender_type, initiator_id, content_type, content, created_at").Where("to_id = ? AND chat_type = ?", req.GroupId, 2)
	}
	var target messageContextRow
	err = query().Where("id = ?", req.MessageId).Take(&target).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, status.Error(codes.NotFound, "source message not found")
	}
	if err != nil {
		return nil, teamGroupDBError(ctx, err)
	}
	var earlier, later []messageContextRow
	if err = query().Where("id < ?", req.MessageId).Order("id DESC").Limit(20).Find(&earlier).Error; err != nil {
		return nil, teamGroupDBError(ctx, err)
	}
	if err = query().Where("id > ?", req.MessageId).Order("id ASC").Limit(20).Find(&later).Error; err != nil {
		return nil, teamGroupDBError(ctx, err)
	}
	rows := make([]messageContextRow, 0, len(earlier)+1+len(later))
	for i := len(earlier) - 1; i >= 0; i-- {
		rows = append(rows, earlier[i])
	}
	rows = append(rows, target)
	rows = append(rows, later...)
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	mentions, err := loadGroupMentionIDs(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	result := &pb.GetTeamGroupMessageContextResponse{TargetMessageId: req.MessageId, Messages: make([]*pb.TeamGroupMessage, 0, len(rows))}
	for _, row := range rows {
		if row.SenderType == 0 {
			row.SenderType = model.MessageSenderUser
		}
		result.Messages = append(result.Messages, &pb.TeamGroupMessage{Id: row.ID, MsgId: row.MsgID, FromId: row.FromID, SenderType: int32(row.SenderType), InitiatorId: row.InitiatorID, ContentType: int32(row.ContentType), Content: row.Content, CreatedAtUnixMs: row.CreatedAt.UTC().UnixMilli(), MentionedUserIds: mentions[row.ID]})
	}
	final, err := s.messageContextAccessSnapshot(ctx, req.TeamId, req.GroupId)
	if err != nil {
		return nil, err
	}
	if final != access {
		return nil, status.Error(codes.PermissionDenied, "team membership changed during context read")
	}
	return result, nil
}
