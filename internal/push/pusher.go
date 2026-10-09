package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/snowflake"
	"github.com/yjydist/go-im/internal/repository"
	"github.com/yjydist/go-im/internal/ws"
	"go.uber.org/zap"
)

const (
	// 内部 HTTP 推送超时
	pushTimeout = 3 * time.Second
)

// Pusher 消息路由与推送
type Pusher struct {
	messageRepo      repository.MessageRepository
	groupRepo        repository.GroupRepository
	teamEligibility  TeamEligibility
	mentionValidator MentionValidator
	redisRepo        repository.RedisRepository
	httpClient       *http.Client
	logger           *zap.Logger
}

func (p *Pusher) SetTeamEligibility(eligibility TeamEligibility) {
	p.teamEligibility = eligibility
}

func (p *Pusher) SetMentionValidator(validator MentionValidator) { p.mentionValidator = validator }

// NewPusher 创建 Pusher
func NewPusher(
	messageRepo repository.MessageRepository,
	groupRepo repository.GroupRepository,
	redisRepo repository.RedisRepository,
	logger *zap.Logger,
) *Pusher {
	return &Pusher{
		messageRepo: messageRepo,
		groupRepo:   groupRepo,
		redisRepo:   redisRepo,
		httpClient: &http.Client{
			Timeout: pushTimeout,
		},
		logger: logger,
	}
}

// HandleMessage 处理从 Kafka 消费到的消息
func (p *Pusher) HandleMessage(ctx context.Context, chatMsg *ws.KafkaChatMsg) error {
	senderType := chatMsg.SenderType
	if senderType == 0 {
		senderType = model.MessageSenderUser // Kafka records written before A43.
	}
	// 1. 入库持久化
	msgModel := &model.Message{
		ID:          snowflake.GenID(),
		MsgID:       chatMsg.MsgID,
		FromID:      chatMsg.FromID,
		SenderType:  senderType,
		InitiatorID: chatMsg.InitiatorID,
		ToID:        chatMsg.ToID,
		ChatType:    int8(chatMsg.ChatType),
		ContentType: int8(chatMsg.ContentType),
		Content:     chatMsg.Content,
	}

	var persistErr error
	if len(chatMsg.MentionedUserIDs) > 0 {
		if err := p.validateMentions(ctx, chatMsg, senderType); err != nil {
			return err
		}
		msgModel.MentionedUserIDs = append([]int64(nil), chatMsg.MentionedUserIDs...)
		persistErr = p.messageRepo.CreateWithMentions(ctx, msgModel, chatMsg.ToID, chatMsg.MentionedUserIDs)
	} else {
		persistErr = p.messageRepo.Create(ctx, msgModel)
	}
	if persistErr != nil {
		return fmt.Errorf("persist message failed: %w", persistErr)
	}

	p.logger.Info("message persisted",
		zap.String("msg_id", chatMsg.MsgID),
		zap.Int64("db_id", msgModel.ID),
	)

	// 2. 路由推送
	switch chatMsg.ChatType {
	case 1: // 单聊
		return p.pushToUser(ctx, chatMsg.ToID, chatMsg.FromID, msgModel)
	case 2: // 群聊
		return p.pushToGroup(ctx, chatMsg.ToID, chatMsg.FromID, msgModel)
	default:
		p.logger.Warn("unknown chat type",
			zap.String("msg_id", chatMsg.MsgID),
			zap.Int("chat_type", chatMsg.ChatType),
		)
		return nil
	}
}

func (p *Pusher) validateMentions(ctx context.Context, chatMsg *ws.KafkaChatMsg, senderType int8) error {
	if senderType != model.MessageSenderUser || chatMsg.ChatType != 2 || chatMsg.ContentType != 1 || chatMsg.FromID <= 0 || chatMsg.ToID <= 0 || len(chatMsg.MentionedUserIDs) > 10 {
		return errors.New("invalid group mention message")
	}
	if p.teamEligibility == nil || p.mentionValidator == nil {
		return errors.New("mention authorization unavailable")
	}
	seen := make(map[int64]struct{}, len(chatMsg.MentionedUserIDs))
	for _, id := range chatMsg.MentionedUserIDs {
		if id <= 0 {
			return errors.New("invalid mention target")
		}
		if _, exists := seen[id]; exists {
			return errors.New("duplicate mention target")
		}
		seen[id] = struct{}{}
	}
	group, err := p.groupRepo.GetByID(ctx, chatMsg.ToID)
	if err != nil {
		return fmt.Errorf("get mention group failed: %w", err)
	}
	if group == nil || group.ID != chatMsg.ToID || group.TeamID == nil || *group.TeamID <= 0 {
		return errors.New("mentions require a team group")
	}
	teamID := *group.TeamID
	senderGeneration, allowed, err := p.teamEligibility.CheckCurrentTeamMember(ctx, teamID, chatMsg.FromID)
	if err != nil {
		return fmt.Errorf("check mention sender failed: %w", err)
	}
	if !allowed || senderGeneration <= 0 {
		return errors.New("mention sender is not a current team member")
	}
	targets := make([]MentionMemberGeneration, 0, len(chatMsg.MentionedUserIDs))
	for _, id := range chatMsg.MentionedUserIDs {
		generation, eligible, err := p.teamEligibility.CheckCurrentTeamMember(ctx, teamID, id)
		if err != nil {
			return fmt.Errorf("check mention target failed: %w", err)
		}
		if !eligible || generation <= 0 {
			return errors.New("mention target is not a current team member")
		}
		targets = append(targets, MentionMemberGeneration{UserID: id, Generation: generation})
	}
	if err := p.mentionValidator.ValidateGroupMentionTargets(ctx, chatMsg.ToID, teamID, chatMsg.FromID, senderGeneration, targets); err != nil {
		return fmt.Errorf("validate group mention targets failed: %w", err)
	}
	return nil
}

// pushToUser 单聊推送：检查在线状态，在线则推送，离线则写离线表
func (p *Pusher) pushToUser(ctx context.Context, toID, fromID int64, msg *model.Message) error {
	// 查询目标用户在线状态
	wsAddr, err := p.redisRepo.GetOnline(ctx, toID)
	if err != nil {
		return fmt.Errorf("get online status failed: %w", err)
	}

	if wsAddr != "" {
		// 用户在线，向 WS 网关发起内部 HTTP 推送
		if err := p.sendPush(ctx, wsAddr, toID, msg); err != nil {
			p.logger.Warn("push to online user failed, saving as offline",
				zap.Int64("user_id", toID),
				zap.Error(err),
			)
			// 推送失败，降级为离线存储
			return p.saveOffline(ctx, toID, msg.ID)
		}
		p.logger.Debug("pushed to online user",
			zap.Int64("user_id", toID),
			zap.String("ws_addr", wsAddr),
		)
		return nil
	}

	// 用户离线，写入离线消息表
	return p.saveOffline(ctx, toID, msg.ID)
}

// pushToGroup 群聊推送：获取群成员，逐个推送
func (p *Pusher) pushToGroup(ctx context.Context, groupID, fromID int64, msg *model.Message) error {
	group, err := p.groupRepo.GetByID(ctx, groupID)
	if err != nil {
		return fmt.Errorf("get group delivery scope failed: %w", err)
	}
	if group == nil || group.ID != groupID {
		return errors.New("invalid group delivery scope")
	}
	if group.TeamID != nil && *group.TeamID <= 0 {
		return errors.New("invalid team group delivery scope")
	}
	if group.TeamID != nil && p.teamEligibility == nil {
		return errors.New("team eligibility service unavailable")
	}
	// 每次投递读取当前群成员，避免成员加入或退出后继续使用旧缓存名单。
	memberIDs, err := p.groupRepo.ListMemberIDs(ctx, groupID)
	if err != nil {
		return fmt.Errorf("get group member ids failed: %w", err)
	}

	// 用户消息跳过本人；机器人 ID 与用户 ID 属于不同身份空间。
	var deliveryErr error
	for _, memberID := range memberIDs {
		if msg.SenderType != model.MessageSenderBot && memberID == fromID {
			continue // 跳过发送者自己
		}

		var err error
		if group.TeamID != nil {
			err = p.pushToTeamMember(ctx, groupID, *group.TeamID, memberID, msg)
		} else {
			err = p.pushToUser(ctx, memberID, fromID, msg)
		}
		if err != nil {
			p.logger.Error("push to group member failed",
				zap.Int64("group_id", groupID),
				zap.Int64("member_id", memberID),
				zap.String("msg_id", msg.MsgID),
				zap.Error(err),
			)
			// 继续处理其他成员，不因单个失败中断
			deliveryErr = errors.Join(deliveryErr, fmt.Errorf("push to group member %d: %w", memberID, err))
		}
	}

	return deliveryErr
}

func (p *Pusher) checkTeamDelivery(ctx context.Context, groupID, teamID, userID int64) (bool, error) {
	if p.teamEligibility == nil {
		return false, errors.New("team eligibility service unavailable")
	}
	generation, eligible, err := p.teamEligibility.CheckCurrentTeamMember(ctx, teamID, userID)
	if err != nil || !eligible {
		return false, err
	}
	if generation <= 0 {
		return false, errors.New("invalid team eligibility generation")
	}
	return p.groupRepo.CheckTeamGroupMemberGeneration(ctx, groupID, teamID, userID, generation)
}

func (p *Pusher) pushToTeamMember(ctx context.Context, groupID, teamID, userID int64, msg *model.Message) error {
	// Redis is only a destination lookup. Authorization happens immediately
	// before each externally visible online or offline delivery attempt.
	wsAddr, err := p.redisRepo.GetOnline(ctx, userID)
	if err != nil {
		return fmt.Errorf("get online status failed: %w", err)
	}
	allowed, err := p.checkTeamDelivery(ctx, groupID, teamID, userID)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	if wsAddr != "" {
		if err := p.sendPush(ctx, wsAddr, userID, msg); err == nil {
			return nil
		}
		p.logger.Warn("team group online push failed; rechecking before offline save", zap.Int64("user_id", userID))
		allowed, err = p.checkTeamDelivery(ctx, groupID, teamID, userID)
		if err != nil {
			return err
		}
		if !allowed {
			return nil
		}
	}
	return p.saveOffline(ctx, userID, msg.ID)
}

// sendPush 向 WS 网关发起内部 HTTP 推送
func (p *Pusher) sendPush(ctx context.Context, wsAddr string, userID int64, msg *model.Message) error {
	chatData, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal chat msg failed: %w", err)
	}
	pushMsg := ws.PushMsg{
		UserID: userID,
		Data:   chatData,
	}

	body, err := json.Marshal(pushMsg)
	if err != nil {
		return fmt.Errorf("marshal push msg failed: %w", err)
	}

	url := fmt.Sprintf("http://%s/internal/push", wsAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create push request failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send push request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("push request returned status %d", resp.StatusCode)
	}

	return nil
}

// saveOffline 保存离线消息
func (p *Pusher) saveOffline(ctx context.Context, userID, messageID int64) error {
	offline := &model.OfflineMessage{
		UserID:    userID,
		MessageID: messageID,
	}

	if err := p.messageRepo.CreateOffline(ctx, offline); err != nil {
		return fmt.Errorf("save offline message failed: %w", err)
	}

	p.logger.Debug("saved offline message",
		zap.Int64("user_id", userID),
		zap.Int64("message_id", messageID),
	)

	return nil
}
