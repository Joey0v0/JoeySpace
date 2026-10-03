package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type botSendIntent struct {
	RunID       int64
	BotID       int64
	InitiatorID int64
	TeamID      int64
	GroupID     int64
	Content     string
}

type botSendRecord struct {
	MsgID           string `gorm:"primaryKey;size:64"`
	RunID           int64
	BotID           int64
	InitiatorID     int64
	TeamID          int64
	GroupID         int64
	Content         string
	TimestampUnixMs int64
	Accepted        bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (botSendRecord) TableName() string { return "im_bot_sends" }

func (r botSendRecord) sameIntent(i botSendIntent) bool {
	return r.RunID == i.RunID && r.BotID == i.BotID && r.InitiatorID == i.InitiatorID &&
		r.TeamID == i.TeamID && r.GroupID == i.GroupID && r.Content == i.Content
}

type botSendStore interface {
	Prepare(context.Context, botSendIntent) (botSendRecord, error)
	MarkAccepted(context.Context, string) error
}

type mysqlBotSendStore struct{ db *gorm.DB }

// Prepare commits a single immutable row before any Kafka write. It does not
// hold a transaction open across the broker request or schedule background work.
func (s *mysqlBotSendStore) Prepare(ctx context.Context, intent botSendIntent) (botSendRecord, error) {
	if s.db == nil {
		return botSendRecord{}, status.Error(codes.Unavailable, "IM send store is unavailable")
	}
	if intent.RunID <= 0 || intent.BotID <= 0 || intent.InitiatorID <= 0 || intent.TeamID <= 0 || intent.GroupID <= 0 {
		return botSendRecord{}, status.Error(codes.InvalidArgument, "invalid bot send scope")
	}
	if _, err := model.DecodeTaskCreatedCard(intent.Content); err != nil {
		return botSendRecord{}, status.Error(codes.InvalidArgument, "invalid task card")
	}
	record := botSendRecord{MsgID: model.BotTaskMsgIDPrefix + strconv.FormatInt(intent.RunID, 10),
		RunID: intent.RunID, BotID: intent.BotID, InitiatorID: intent.InitiatorID, TeamID: intent.TeamID,
		GroupID: intent.GroupID, Content: intent.Content, TimestampUnixMs: time.Now().UnixMilli()}
	// One INSERT is atomic; no second write must be committed with it.
	err := s.db.WithContext(ctx).Session(&gorm.Session{SkipDefaultTransaction: true}).Create(&record).Error
	if err == nil {
		return record, nil
	}
	var duplicate *driver.MySQLError
	if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
		return botSendRecord{}, teamGroupDBError(ctx, err)
	}
	var stored botSendRecord
	if err := s.db.WithContext(ctx).Where("msg_id = ?", record.MsgID).Take(&stored).Error; err != nil {
		return botSendRecord{}, teamGroupDBError(ctx, err)
	}
	if !stored.sameIntent(intent) {
		return botSendRecord{}, status.Error(codes.AlreadyExists, "message ID belongs to a different bot send")
	}
	if stored.TimestampUnixMs <= 0 {
		return botSendRecord{}, status.Error(codes.Internal, "invalid stored bot send")
	}
	return stored, nil
}

func (s *mysqlBotSendStore) MarkAccepted(ctx context.Context, msgID string) error {
	if s.db == nil {
		return status.Error(codes.Unavailable, "IM send store is unavailable")
	}
	result := s.db.WithContext(ctx).Session(&gorm.Session{SkipDefaultTransaction: true}).
		Model(&botSendRecord{}).Where("msg_id = ?", msgID).Update("accepted", true)
	if result.Error != nil {
		return teamGroupDBError(ctx, result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}
	// Concurrent or repeated acknowledgements may report zero changed rows.
	var stored botSendRecord
	if err := s.db.WithContext(ctx).Select("accepted").Where("msg_id = ?", msgID).Take(&stored).Error; err != nil {
		return teamGroupDBError(ctx, err)
	}
	if !stored.Accepted {
		return status.Error(codes.Internal, "bot send acceptance was not saved")
	}
	return nil
}
