package agent

import (
	"context"
	"strconv"

	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

func collectionReplyIntent(collection taskDraftCollection, index int32) (draftReplyRecord, error) {
	if collection.ID <= 0 || index < 0 || index >= maxGeneratedTaskDrafts || len(collection.Items) < 1 || len(collection.Items) > maxGeneratedTaskDrafts || int(index) >= len(collection.Items) {
		return draftReplyRecord{}, status.Error(codes.FailedPrecondition, "task item result is not saved")
	}
	run := collection.Items[index]
	validated, err := newWaitingTaskDraftRun(run.Scope, run.Draft)
	if run.ID != collection.ID || run.Scope != collection.Scope || run.Revision <= 0 || run.Status != draftSucceeded ||
		!validCollectionItemTaskState(run, index) || run.Draft.AssigneeResolution == "" || run.Draft.Deadline.Resolution == "" || err != nil || validated.Draft != run.Draft {
		return draftReplyRecord{}, status.Error(codes.FailedPrecondition, "task item result is not saved")
	}
	msgID, err := model.BotTaskItemMsgID(collection.ID, index)
	if err != nil {
		return draftReplyRecord{}, status.Error(codes.FailedPrecondition, "invalid task item identity")
	}
	content, err := model.EncodeTaskCreatedCard(run.TaskID, run.Draft.Title)
	if err != nil {
		return draftReplyRecord{}, status.Error(codes.Unavailable, "stored task item result is invalid")
	}
	return draftReplyRecord{RunID: run.ID, ItemIndex: index, TaskID: run.TaskID, TeamID: run.Scope.TeamID,
		GroupID: run.Scope.GroupID, InitiatorID: run.Scope.InitiatorID, MsgID: msgID, Content: content}, nil
}

func (r draftReplyRecord) matchesCollection(collection taskDraftCollection, index int32) bool {
	expected, err := collectionReplyIntent(collection, index)
	r.Accepted = false
	return err == nil && r == expected
}

const selectDraftCollectionReply = `SELECT run_id, item_index, task_id, team_id, group_id, initiator_id, msg_id, content, accepted
    FROM agent_task_replies WHERE run_id = ? AND item_index = ? AND initiator_id = ?`

const insertDraftCollectionReply = `INSERT INTO agent_task_replies
    (run_id, item_index, task_id, team_id, group_id, initiator_id, msg_id, content, accepted)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`

const acceptDraftCollectionReply = `UPDATE agent_task_replies SET accepted = 1
    WHERE run_id = ? AND item_index = ? AND task_id = ? AND team_id = ? AND group_id = ? AND initiator_id = ? AND BINARY msg_id = BINARY ? AND BINARY content = BINARY ? AND accepted = 0`

// Scan every required column explicitly so a NULL index cannot become item 0.
func queryDraftCollectionReply(db *gorm.DB, runID int64, index int32, actorID int64) (draftReplyRecord, bool, error) {
	rows, err := db.Raw(selectDraftCollectionReply, runID, index, actorID).Rows()
	if err != nil {
		return draftReplyRecord{}, false, err
	}
	defer rows.Close()
	var record draftReplyRecord
	found := false
	for rows.Next() {
		if found {
			return draftReplyRecord{}, false, status.Error(codes.Unavailable, "stored item reply has multiple records")
		}
		if err := rows.Scan(&record.RunID, &record.ItemIndex, &record.TaskID, &record.TeamID, &record.GroupID, &record.InitiatorID, &record.MsgID, &record.Content, &record.Accepted); err != nil {
			return draftReplyRecord{}, false, err
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return draftReplyRecord{}, false, err
	}
	return record, found, nil
}

func (s *draftStore) prepareCollectionReply(ctx context.Context, authorized taskDraftCollection, index int32) (draftReplyRecord, error) {
	if s == nil || s.db == nil {
		return draftReplyRecord{}, status.Error(codes.Unavailable, "reply storage unavailable")
	}
	expected, err := collectionReplyIntent(authorized, index)
	if err != nil {
		return draftReplyRecord{}, err
	}
	var record draftReplyRecord
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		saved, err := queryDraftCollection(ctx, tx, authorized.ID, authorized.Scope.InitiatorID, true, nil)
		if err != nil {
			return err
		}
		if saved.ID != authorized.ID || saved.Scope != authorized.Scope || len(saved.Items) != len(authorized.Items) || saved.Items[index] != authorized.Items[index] {
			return status.Error(codes.Aborted, "task item result changed; reload before posting")
		}
		existing, found, err := queryDraftCollectionReply(tx, saved.ID, index, saved.Scope.InitiatorID)
		if err != nil {
			return err
		}
		if found {
			if !existing.matchesCollection(saved, index) {
				return status.Error(codes.AlreadyExists, "reply intent does not match task item result")
			}
			record = existing
			return nil
		}
		result := tx.Exec(insertDraftCollectionReply, expected.RunID, expected.ItemIndex, expected.TaskID, expected.TeamID, expected.GroupID, expected.InitiatorID, expected.MsgID, expected.Content, false)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return status.Error(codes.Unavailable, "reply intent was not saved")
		}
		record = expected
		return nil
	})
	if err != nil {
		return draftReplyRecord{}, draftConfirmationStorageError(ctx, err)
	}
	return record, nil
}

func (s *draftStore) loadCollectionReply(ctx context.Context, collection taskDraftCollection, index int32) (draftReplyRecord, bool, error) {
	if s == nil || s.db == nil {
		return draftReplyRecord{}, false, status.Error(codes.Unavailable, "reply storage unavailable")
	}
	if _, err := collectionReplyIntent(collection, index); err != nil {
		return draftReplyRecord{}, false, err
	}
	record, found, err := queryDraftCollectionReply(s.db.WithContext(ctx), collection.ID, index, collection.Scope.InitiatorID)
	if err != nil {
		return draftReplyRecord{}, false, draftStorageError(ctx, err)
	}
	if found && !record.matchesCollection(collection, index) {
		return draftReplyRecord{}, false, status.Error(codes.Unavailable, "stored item reply is invalid")
	}
	return record, found, nil
}

func (s *draftStore) acceptCollectionReply(ctx context.Context, record draftReplyRecord) error {
	if s == nil || s.db == nil {
		return status.Error(codes.Unavailable, "reply storage unavailable")
	}
	msgID, err := model.BotTaskItemMsgID(record.RunID, record.ItemIndex)
	card, cardErr := model.DecodeTaskCreatedCard(record.Content)
	if err != nil || record.MsgID != msgID || record.TaskID <= 0 || record.TeamID <= 0 || record.GroupID <= 0 || record.InitiatorID <= 0 || cardErr != nil || card.TaskID != strconv.FormatInt(record.TaskID, 10) {
		return status.Error(codes.FailedPrecondition, "invalid item reply acceptance")
	}
	result := s.db.WithContext(ctx).Exec(acceptDraftCollectionReply, record.RunID, record.ItemIndex, record.TaskID, record.TeamID, record.GroupID, record.InitiatorID, record.MsgID, record.Content)
	if result.Error != nil {
		return draftStorageError(ctx, result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	if result.RowsAffected != 0 {
		return status.Error(codes.Unavailable, "item reply acceptance affected invalid record count")
	}
	saved, found, err := queryDraftCollectionReply(s.db.WithContext(ctx), record.RunID, record.ItemIndex, record.InitiatorID)
	if err != nil {
		return draftStorageError(ctx, err)
	}
	expected := record
	expected.Accepted = true
	if !found || saved != expected {
		return status.Error(codes.Unavailable, "item reply acceptance was not saved")
	}
	return nil
}
