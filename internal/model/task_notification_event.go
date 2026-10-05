package model

import (
	"errors"
	"strconv"
)

const TaskNotificationEventVersion = 1
const TaskNotificationEventType = "task_notification_changed"

// TaskNotificationEvent is a refresh hint, never user authorization or task content.
// The notification ID is the stable event ID; all IDs use JSON strings.
type TaskNotificationEvent struct {
	Version        int    `json:"version"`
	Type           string `json:"type"`
	NotificationID int64  `json:"notification_id,string"`
	TeamID         int64  `json:"team_id,string"`
	RecipientID    int64  `json:"recipient_id,string"`
}

func (e TaskNotificationEvent) Validate() error {
	if e.Version != TaskNotificationEventVersion || e.Type != TaskNotificationEventType || e.NotificationID <= 0 || e.TeamID <= 0 || e.RecipientID <= 0 {
		return errors.New("invalid task notification event")
	}
	return nil
}

func (e TaskNotificationEvent) Key() string {
	return "task-notification:" + strconv.FormatInt(e.NotificationID, 10)
}
