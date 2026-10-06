package main

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/rpcauth"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

const triggerContextTimeout = 5 * time.Second
const triggerContextMaxBytes = 2 * 1024 * 1024

const triggerOutboxQuery = `SELECT message_id, action, event_version, msg_id, actor_id, team_id, group_id, instruction, reference_time_ms, published
    FROM im_agent_trigger_outbox WHERE message_id = ? LIMIT 2`
const triggerSourceQuery = `SELECT id, msg_id, from_id, sender_type, initiator_id, to_id, chat_type, content_type, content, created_at
    FROM messages WHERE id = ? LIMIT 2`
const triggerMembershipQuery = "SELECT `groups`.id, `groups`.team_id, group_members.user_id\n" +
	"    FROM `groups` JOIN group_members ON group_members.group_id = `groups`.id\n" +
	"    WHERE `groups`.id = ? AND `groups`.team_id = ? AND group_members.user_id = ? LIMIT 2"
const triggerHistoryQuery = "SELECT m.id, m.msg_id, m.from_id, m.sender_type, m.initiator_id, m.to_id, m.chat_type, m.content_type, m.content, m.created_at\n" +
	"    FROM messages AS m\n" +
	"    JOIN `groups` ON `groups`.id = m.to_id AND `groups`.team_id = ?\n" +
	"    JOIN group_members ON group_members.group_id = `groups`.id AND group_members.user_id = ?\n" +
	"    WHERE m.to_id = ? AND m.chat_type = 2 AND m.id <= ? ORDER BY m.id DESC LIMIT 20"

func triggerContextError(ctx context.Context, code codes.Code, message string) error {
	if err := ctx.Err(); err != nil {
		return status.FromContextError(err).Err()
	}
	return status.Error(code, message)
}

func validTriggerMessage(msg *model.Message) bool {
	if msg.SenderType == 0 {
		msg.SenderType = model.MessageSenderUser
	}
	return msg.ID > 0 && msg.FromID > 0 && msg.ToID > 0 && msg.ChatType == 2 && msg.CreatedAt.UnixMilli() > 0 &&
		msg.MsgID != "" && len(msg.MsgID) <= 64 && utf8.ValidString(msg.MsgID) && !strings.ContainsAny(msg.MsgID, "\x00\r\n") &&
		msg.ContentType >= 1 && msg.ContentType <= 4 && utf8.ValidString(msg.Content) && len(msg.Content) <= 65535 &&
		(msg.SenderType == model.MessageSenderUser && msg.InitiatorID == 0 || msg.SenderType == model.MessageSenderBot && msg.InitiatorID > 0)
}

func readTriggerMessages(ctx context.Context, db *gorm.DB, query string, maxRows int, args ...any) ([]model.Message, error) {
	rows, err := db.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, triggerContextError(ctx, codes.Unavailable, "trigger message storage unavailable")
	}
	defer rows.Close()
	var messages []model.Message
	for rows.Next() {
		var m model.Message
		if len(messages) >= maxRows || rows.Scan(&m.ID, &m.MsgID, &m.FromID, &m.SenderType, &m.InitiatorID, &m.ToID, &m.ChatType, &m.ContentType, &m.Content, &m.CreatedAt) != nil || !validTriggerMessage(&m) {
			return nil, triggerContextError(ctx, codes.Unavailable, "saved trigger message is invalid")
		}
		messages = append(messages, m)
	}
	if rows.Err() != nil || ctx.Err() != nil {
		return nil, triggerContextError(ctx, codes.Unavailable, "trigger message storage unavailable")
	}
	return messages, nil
}

func readTriggerOutbox(ctx context.Context, db *gorm.DB, messageID int64) (model.AgentTriggerOutbox, error) {
	rows, err := db.WithContext(ctx).Raw(triggerOutboxQuery, messageID).Rows()
	if err != nil {
		return model.AgentTriggerOutbox{}, triggerContextError(ctx, codes.Unavailable, "trigger storage unavailable")
	}
	defer rows.Close()
	var outbox model.AgentTriggerOutbox
	found := false
	for rows.Next() {
		if found || rows.Scan(&outbox.MessageID, &outbox.Action, &outbox.EventVersion, &outbox.MsgID, &outbox.ActorID, &outbox.TeamID, &outbox.GroupID, &outbox.Instruction, &outbox.ReferenceTimeMS, &outbox.Published) != nil || outbox.Validate() != nil || outbox.MessageID != messageID {
			return model.AgentTriggerOutbox{}, triggerContextError(ctx, codes.Unavailable, "saved trigger is invalid")
		}
		found = true
	}
	if rows.Err() != nil || ctx.Err() != nil {
		return model.AgentTriggerOutbox{}, triggerContextError(ctx, codes.Unavailable, "trigger storage unavailable")
	}
	if !found {
		return model.AgentTriggerOutbox{}, triggerContextError(ctx, codes.NotFound, "saved trigger not found")
	}
	return outbox, nil
}

func checkTriggerGroup(ctx context.Context, db *gorm.DB, outbox model.AgentTriggerOutbox) error {
	rows, err := db.WithContext(ctx).Raw(triggerMembershipQuery, outbox.GroupID, outbox.TeamID, outbox.ActorID).Rows()
	if err != nil {
		return triggerContextError(ctx, codes.Unavailable, "trigger group storage unavailable")
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var groupID, teamID, actorID int64
		if found || rows.Scan(&groupID, &teamID, &actorID) != nil || groupID <= 0 || teamID <= 0 || actorID <= 0 {
			return triggerContextError(ctx, codes.Unavailable, "saved trigger group is invalid")
		}
		if groupID != outbox.GroupID || teamID != outbox.TeamID || actorID != outbox.ActorID {
			return triggerContextError(ctx, codes.PermissionDenied, "current trigger group membership required")
		}
		found = true
	}
	if rows.Err() != nil || ctx.Err() != nil {
		return triggerContextError(ctx, codes.Unavailable, "trigger group storage unavailable")
	}
	if !found {
		return triggerContextError(ctx, codes.PermissionDenied, "current trigger group membership required")
	}
	return nil
}

func sameTriggerSource(left, right model.Message) bool {
	return left.ID == right.ID && left.MsgID == right.MsgID && left.FromID == right.FromID && left.SenderType == right.SenderType &&
		left.InitiatorID == right.InitiatorID && left.ToID == right.ToID && left.ChatType == right.ChatType &&
		left.ContentType == right.ContentType && left.Content == right.Content && left.CreatedAt.Equal(right.CreatedAt)
}

func (s *triggerContextServer) currentTriggerGeneration(ctx context.Context, outbox model.AgentTriggerOutbox) (int64, error) {
	generation, err := s.teams.CheckGeneration(ctx, outbox.ActorID, outbox.TeamID)
	if err != nil {
		code := status.Code(err)
		if code != codes.PermissionDenied && code != codes.Canceled && code != codes.DeadlineExceeded {
			code = codes.Unavailable
		}
		return 0, triggerContextError(ctx, code, "current trigger team qualification unavailable")
	}
	if generation <= 0 || ctx.Err() != nil {
		return 0, triggerContextError(ctx, codes.Unavailable, "current trigger team generation unavailable")
	}
	return generation, nil
}

func (s *triggerContextServer) ReadTaskTriggerContext(ctx context.Context, req *pb.ReadTaskTriggerContextRequest) (*pb.ReadTaskTriggerContextResponse, error) {
	if ctx == nil {
		return nil, status.Error(codes.Unauthenticated, "verified service TLS identity required")
	}
	name := ""
	if s != nil {
		name = s.agentDNSName
	}
	if err := rpcauth.RequireServiceIdentity(ctx, name); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	messageID := req.GetMessageId()
	if messageID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "positive trigger message ID required")
	}
	if s == nil || s.db == nil || s.teams == nil {
		return nil, status.Error(codes.Unavailable, "trigger context is not enabled")
	}
	ctx, cancel := context.WithTimeout(ctx, triggerContextTimeout)
	defer cancel()
	outbox, err := readTriggerOutbox(ctx, s.db, messageID)
	if err != nil {
		return nil, err
	}
	sources, err := readTriggerMessages(ctx, s.db, triggerSourceQuery, 2, messageID)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, triggerContextError(ctx, codes.NotFound, "saved trigger source not found")
	}
	if len(sources) != 1 {
		return nil, triggerContextError(ctx, codes.Unavailable, "saved trigger source is invalid")
	}
	source := sources[0]
	instruction, command := model.AgentTaskCommand(&source)
	if !command || source.ID != outbox.MessageID || source.MsgID != outbox.MsgID || source.FromID != outbox.ActorID || source.ToID != outbox.GroupID ||
		instruction != outbox.Instruction || source.CreatedAt.UnixMilli() != outbox.ReferenceTimeMS {
		return nil, triggerContextError(ctx, codes.Unavailable, "saved trigger source does not match")
	}
	if err := checkTriggerGroup(ctx, s.db, outbox); err != nil {
		return nil, err
	}
	generation, err := s.currentTriggerGeneration(ctx, outbox)
	if err != nil {
		return nil, err
	}
	if err := checkTeamGroupReadGeneration(ctx, s.db, outbox.GroupID, outbox.TeamID, outbox.ActorID, generation); err != nil {
		return nil, err
	}
	messages, err := readTriggerMessages(ctx, s.db, triggerHistoryQuery, 20, outbox.TeamID, outbox.ActorID, outbox.GroupID, outbox.MessageID)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, triggerContextError(ctx, codes.PermissionDenied, "current trigger history is not accessible")
	}
	if !sameTriggerSource(source, messages[0]) {
		return nil, triggerContextError(ctx, codes.Unavailable, "saved trigger source changed while reading")
	}
	response := &pb.ReadTaskTriggerContextResponse{MessageId: outbox.MessageID, MsgId: outbox.MsgID, ActorId: outbox.ActorID, TeamId: outbox.TeamID,
		GroupId: outbox.GroupID, Instruction: outbox.Instruction, ReferenceTimeUnixMs: outbox.ReferenceTimeMS, RequestKey: outbox.RequestKey()}
	msgIDs := make(map[string]bool, len(messages))
	for i, m := range messages {
		if m.ID > outbox.MessageID || m.ToID != outbox.GroupID || msgIDs[m.MsgID] || i > 0 && m.ID >= messages[i-1].ID {
			return nil, triggerContextError(ctx, codes.Unavailable, "saved trigger history is invalid")
		}
		msgIDs[m.MsgID] = true
		response.Messages = append(response.Messages, &pb.TeamGroupMessage{Id: m.ID, MsgId: m.MsgID, FromId: m.FromID, SenderType: int32(m.SenderType),
			InitiatorId: m.InitiatorID, ContentType: int32(m.ContentType), Content: m.Content, CreatedAtUnixMs: m.CreatedAt.UnixMilli()})
	}
	if proto.Size(response) > triggerContextMaxBytes || ctx.Err() != nil {
		return nil, triggerContextError(ctx, codes.Unavailable, "trigger context exceeds its response limit")
	}
	currentGeneration, err := s.currentTriggerGeneration(ctx, outbox)
	if err != nil {
		return nil, err
	}
	if currentGeneration != generation {
		return nil, triggerContextError(ctx, codes.PermissionDenied, "trigger team generation changed while reading")
	}
	if err := checkTeamGroupReadGeneration(ctx, s.db, outbox.GroupID, outbox.TeamID, outbox.ActorID, currentGeneration); err != nil {
		return nil, err
	}
	return response, nil
}
