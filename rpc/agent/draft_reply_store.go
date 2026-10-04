package agent

import (
	"context"
	"strconv"

	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

type draftReplyRecord struct {
	RunID       int64
	ItemIndex   int32
	TaskID      int64
	TeamID      int64
	GroupID     int64
	InitiatorID int64
	MsgID       string
	Content     string
	Accepted    bool
}

func replyIntent(run taskDraftRun) (draftReplyRecord, error) {
	if run.ID <= 0 || run.Status != draftSucceeded || run.TaskID <= 0 ||
		run.TaskRequestKey != draftTaskRequestKey(run.ID) || run.Scope.TeamID <= 0 ||
		run.Scope.GroupID <= 0 || run.Scope.InitiatorID <= 0 {
		return draftReplyRecord{}, status.Error(codes.FailedPrecondition, "task result is not saved")
	}
	content, err := model.EncodeTaskCreatedCard(run.TaskID, run.Draft.Title)
	if err != nil {
		return draftReplyRecord{}, status.Error(codes.Unavailable, "stored task result is invalid")
	}
	return draftReplyRecord{RunID: run.ID, TaskID: run.TaskID, TeamID: run.Scope.TeamID,
		GroupID: run.Scope.GroupID, InitiatorID: run.Scope.InitiatorID,
		MsgID: model.BotTaskMsgIDPrefix + strconv.FormatInt(run.ID, 10), Content: content}, nil
}

func (r draftReplyRecord) matches(run taskDraftRun) bool {
	expected, err := replyIntent(run)
	r.Accepted = false
	return err == nil && r == expected
}

type draftReplyStore interface {
	prepareReply(context.Context, taskDraftRun) (draftReplyRecord, error)
	loadReply(context.Context, taskDraftRun) (draftReplyRecord, bool, error)
	acceptReply(context.Context, draftReplyRecord) error
}

const selectDraftReply = `SELECT run_id, task_id, team_id, group_id, initiator_id, msg_id, content, accepted
    FROM agent_task_replies WHERE run_id = ? AND initiator_id = ? AND item_index = 0`

// The run lock serializes preparation with confirmation and freezes one reply
// from the committed task result. No network call runs inside this transaction.
func (s *draftStore) prepareReply(ctx context.Context, authorized taskDraftRun) (draftReplyRecord, error) {
	if s == nil || s.db == nil {
		return draftReplyRecord{}, status.Error(codes.Unavailable, "reply storage unavailable")
	}
	expected, err := replyIntent(authorized)
	if err != nil {
		return draftReplyRecord{}, err
	}
	var record draftReplyRecord
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		saved, err := lockedDraft(tx, authorized.ID, authorized.Scope.InitiatorID)
		if err != nil {
			return err
		}
		if saved != authorized {
			return status.Error(codes.Aborted, "task result changed; reload before posting")
		}
		result := tx.Raw(selectDraftReply, saved.ID, saved.Scope.InitiatorID).Scan(&record)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 0 {
			if !record.matches(saved) {
				return status.Error(codes.AlreadyExists, "reply intent does not match task result")
			}
			return nil
		}
		record = expected
		return tx.Exec(`INSERT INTO agent_task_replies
            (run_id, task_id, team_id, group_id, initiator_id, msg_id, content, accepted)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, record.RunID, record.TaskID, record.TeamID,
			record.GroupID, record.InitiatorID, record.MsgID, record.Content, false).Error
	})
	if err != nil {
		return draftReplyRecord{}, draftConfirmationStorageError(ctx, err)
	}
	return record, nil
}

func (s *draftStore) loadReply(ctx context.Context, run taskDraftRun) (draftReplyRecord, bool, error) {
	if s == nil || s.db == nil {
		return draftReplyRecord{}, false, status.Error(codes.Unavailable, "reply storage unavailable")
	}
	var record draftReplyRecord
	result := s.db.WithContext(ctx).Raw(selectDraftReply, run.ID, run.Scope.InitiatorID).Scan(&record)
	if result.Error != nil {
		return draftReplyRecord{}, false, draftStorageError(ctx, result.Error)
	}
	if result.RowsAffected == 0 {
		return draftReplyRecord{}, false, nil
	}
	if !record.matches(run) {
		return draftReplyRecord{}, false, status.Error(codes.Unavailable, "stored reply is invalid")
	}
	return record, true, nil
}

func (s *draftStore) acceptReply(ctx context.Context, record draftReplyRecord) error {
	if record.ItemIndex != 0 {
		return status.Error(codes.FailedPrecondition, "single reply requires item zero")
	}
	if s == nil || s.db == nil {
		return status.Error(codes.Unavailable, "reply storage unavailable")
	}
	result := s.db.WithContext(ctx).Exec(`UPDATE agent_task_replies SET accepted = 1
		WHERE run_id = ? AND initiator_id = ? AND msg_id = ? AND item_index = 0 AND accepted = 0`, record.RunID, record.InitiatorID, record.MsgID)
	if result.Error != nil {
		return draftStorageError(ctx, result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var saved draftReplyRecord
	result = s.db.WithContext(ctx).Raw(selectDraftReply, record.RunID, record.InitiatorID).Scan(&saved)
	if result.Error != nil {
		return draftStorageError(ctx, result.Error)
	}
	expected := record
	expected.Accepted = true
	if result.RowsAffected != 1 || saved != expected {
		return status.Error(codes.Unavailable, "reply acceptance was not saved")
	}
	return nil
}
