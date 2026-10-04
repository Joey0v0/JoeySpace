package repository

import (
	"context"
	"errors"

	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"gorm.io/gorm"
)

type agentTriggerMessageRepository struct {
	*messageRepository
}

func NewAgentTriggerMessageRepository(db *gorm.DB) MessageRepository {
	return &agentTriggerMessageRepository{messageRepository: &messageRepository{db: db}}
}

const agentTriggerColumns = `message_id, action, event_version, msg_id, actor_id, team_id, group_id, instruction, reference_time_ms, published`
const insertAgentTrigger = `INSERT INTO im_agent_trigger_outbox
    (message_id, action, event_version, msg_id, actor_id, team_id, group_id, instruction, reference_time_ms, published)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
const selectAgentTrigger = `SELECT ` + agentTriggerColumns + ` FROM im_agent_trigger_outbox WHERE message_id = ?`
const selectPendingAgentTriggers = `SELECT ` + agentTriggerColumns + ` FROM im_agent_trigger_outbox WHERE published = 0 ORDER BY message_id ASC LIMIT ?`
const publishAgentTrigger = `UPDATE im_agent_trigger_outbox SET published = 1 WHERE message_id = ? AND published = 0`

func sameAgentTrigger(left, right model.AgentTriggerOutbox) bool {
	left.Published, right.Published = false, false
	return left == right
}

// The authoritative source time is reread after insertion. The in-memory
// timestamp can have more precision than the messages TIMESTAMP column.
func (r *agentTriggerMessageRepository) Create(ctx context.Context, msg *model.Message) error {
	if r == nil || r.messageRepository == nil || r.db == nil {
		return errors.New("agent trigger message storage unavailable")
	}
	instruction, candidate := model.AgentTaskCommand(msg)
	if !candidate {
		return r.messageRepository.Create(ctx, msg)
	}
	saved := *msg
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := (&messageRepository{db: tx}).Create(ctx, &saved); err != nil {
			return err
		}
		var source model.Message
		if err := tx.Where("id = ?", saved.ID).Take(&source).Error; err != nil {
			return errors.New("saved trigger source could not be read")
		}
		sourceInstruction, valid := model.AgentTaskCommand(&source)
		if !valid || source.ID <= 0 || source.ID != saved.ID || source.MsgID != saved.MsgID || source.FromID != saved.FromID || source.ToID != saved.ToID || source.Content != saved.Content || sourceInstruction != instruction {
			return errors.New("saved trigger source does not match message")
		}
		var group model.Group
		if err := tx.Select("team_id").Where("id = ?", source.ToID).Take(&group).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				saved.CreatedAt = source.CreatedAt
				return nil // Missing or old groups remain ordinary messages.
			}
			return errors.New("trigger group could not be read")
		}
		saved.CreatedAt = source.CreatedAt
		if group.TeamID == nil || *group.TeamID <= 0 {
			return nil
		}
		intent := model.AgentTriggerOutbox{MessageID: source.ID, Action: model.AgentTriggerAction, EventVersion: model.AgentTriggerVersion,
			MsgID: source.MsgID, ActorID: source.FromID, TeamID: *group.TeamID, GroupID: source.ToID,
			Instruction: sourceInstruction, ReferenceTimeMS: source.CreatedAt.UnixMilli()}
		if err := intent.Validate(); err != nil {
			return err
		}
		result := tx.Exec(insertAgentTrigger, intent.MessageID, intent.Action, intent.EventVersion, intent.MsgID, intent.ActorID,
			intent.TeamID, intent.GroupID, intent.Instruction, intent.ReferenceTimeMS, false)
		if result.Error == nil {
			if result.RowsAffected != 1 {
				return errors.New("trigger intent was not saved")
			}
			return nil
		}
		var duplicate *mysql.MySQLError
		if !errors.As(result.Error, &duplicate) || duplicate.Number != 1062 {
			return errors.New("trigger intent could not be saved")
		}
		rows, err := readAgentTriggers(tx, selectAgentTrigger+" FOR UPDATE", intent.MessageID)
		if err != nil || len(rows) != 1 || rows[0].Validate() != nil || !sameAgentTrigger(rows[0], intent) {
			return errors.New("trigger intent conflicts with saved facts")
		}
		return nil
	})
	if err != nil {
		return err
	}
	*msg = saved
	return nil
}

type agentTriggerStore struct {
	db *gorm.DB
}

func NewAgentTriggerStore(db *gorm.DB) AgentTriggerStore { return &agentTriggerStore{db: db} }

// Explicit scanning rejects NULL required fields instead of treating them as
// valid zero values. Callers also check cardinality and immutable facts.
func readAgentTriggers(db *gorm.DB, query string, args ...any) ([]model.AgentTriggerOutbox, error) {
	rows, err := db.Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []model.AgentTriggerOutbox
	for rows.Next() {
		var r model.AgentTriggerOutbox
		if err := rows.Scan(&r.MessageID, &r.Action, &r.EventVersion, &r.MsgID, &r.ActorID, &r.TeamID, &r.GroupID, &r.Instruction, &r.ReferenceTimeMS, &r.Published); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func (s *agentTriggerStore) ListPending(ctx context.Context, limit int) ([]model.AgentTriggerOutbox, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("agent trigger storage unavailable")
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("agent trigger limit must be between 1 and 100")
	}
	rows, err := readAgentTriggers(s.db.WithContext(ctx), selectPendingAgentTriggers, limit)
	if err != nil {
		return nil, errors.New("pending agent triggers could not be read")
	}
	if len(rows) > limit {
		return nil, errors.New("pending agent trigger count is invalid")
	}
	var previous int64
	for _, row := range rows {
		if row.Validate() != nil || row.Published || row.MessageID <= previous {
			return nil, errors.New("pending agent trigger is invalid")
		}
		previous = row.MessageID
	}
	return rows, nil
}

func (s *agentTriggerStore) MarkPublished(ctx context.Context, expected model.AgentTriggerOutbox) error {
	if s == nil || s.db == nil {
		return errors.New("agent trigger storage unavailable")
	}
	if err := expected.Validate(); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rows, err := readAgentTriggers(tx, selectAgentTrigger+" FOR UPDATE", expected.MessageID)
		if err != nil || len(rows) != 1 || rows[0].Validate() != nil || !sameAgentTrigger(rows[0], expected) {
			return errors.New("agent trigger publication facts do not match")
		}
		if rows[0].Published {
			return nil
		}
		result := tx.Exec(publishAgentTrigger, expected.MessageID)
		if result.Error != nil || result.RowsAffected != 1 {
			return errors.New("agent trigger publication was not saved")
		}
		return nil
	})
}
