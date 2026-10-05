package main

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type gormTaskNotificationOutboxStore struct{ db *gorm.DB }

func newTaskNotificationOutboxStore(db *gorm.DB) taskNotificationOutboxStore {
	return &gormTaskNotificationOutboxStore{db: db}
}

func (s *gormTaskNotificationOutboxStore) ListPending(ctx context.Context, limit int) ([]taskNotificationOutboxRow, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("task notification outbox unavailable")
	}
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid task notification outbox limit")
	}
	rows := make([]taskNotificationOutboxRow, 0)
	err := s.db.WithContext(ctx).Select("notification_id, team_id, recipient_id, event_version, published").
		Where("published = ?", false).Order("notification_id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, notificationOutboxDatabaseError(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rows) > limit {
		return nil, errors.New("invalid task notification outbox result")
	}
	for i, row := range rows {
		if row.Validate() != nil || row.Published || (i > 0 && row.NotificationID <= rows[i-1].NotificationID) {
			return nil, errors.New("invalid task notification outbox result")
		}
	}
	return rows, nil
}

func (s *gormTaskNotificationOutboxStore) MarkPublished(ctx context.Context, row taskNotificationOutboxRow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return errors.New("task notification outbox unavailable")
	}
	if err := row.Validate(); err != nil {
		return err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored taskNotificationOutboxRow
		if err := tx.Select("notification_id, team_id, recipient_id, event_version, published").
			Clauses(clause.Locking{Strength: "UPDATE"}).Where("notification_id = ?", row.NotificationID).Take(&stored).Error; err != nil {
			return err
		}
		if stored.Validate() != nil || stored.NotificationID != row.NotificationID || stored.TeamID != row.TeamID || stored.RecipientID != row.RecipientID || stored.EventVersion != row.EventVersion {
			return errors.New("task notification outbox facts conflict")
		}
		if stored.Published {
			return nil
		}
		updated := tx.Model(&taskNotificationOutboxRow{}).
			Where("notification_id = ? AND team_id = ? AND recipient_id = ? AND event_version = ? AND published = ?", row.NotificationID, row.TeamID, row.RecipientID, row.EventVersion, false).
			Updates(map[string]any{"published": true, "published_at": gorm.Expr("CURRENT_TIMESTAMP")})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("task notification outbox confirmation did not affect one row")
		}
		return nil
	})
	if err != nil {
		return notificationOutboxDatabaseError(ctx)
	}
	return ctx.Err()
}

func notificationOutboxDatabaseError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("task notification outbox unavailable")
}
