package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

const TaskNotificationPushPath = "/internal/task-notifications"
const TaskNotificationMaxWireBytes = 1024
const TaskNotificationQueued = "queued"
const TaskNotificationOffline = "offline"
const TaskNotificationDenied = "denied"

// TaskNotificationDelivery reports a transport outcome, never Kafka commit or browser receipt.
type TaskNotificationDelivery struct {
	NotificationID int64  `json:"notification_id,string"`
	Outcome        string `json:"outcome"`
}

func (d TaskNotificationDelivery) Validate(expectedID int64) error {
	if expectedID <= 0 || d.NotificationID != expectedID || (d.Outcome != TaskNotificationQueued && d.Outcome != TaskNotificationOffline && d.Outcome != TaskNotificationDenied) {
		return errors.New("invalid task notification delivery response")
	}
	return nil
}

// DecodeTaskNotificationEvent rejects duplicate/unknown fields, numeric or noncanonical IDs and extra JSON values.
func DecodeTaskNotificationEvent(wire []byte) (TaskNotificationEvent, error) {
	var event TaskNotificationEvent
	invalid := errors.New("invalid task notification event")
	if len(wire) == 0 || len(wire) > TaskNotificationMaxWireBytes || !utf8.Valid(wire) {
		return event, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(wire))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return event, invalid
	}
	seen := make(map[string]bool, 5)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return TaskNotificationEvent{}, invalid
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return TaskNotificationEvent{}, invalid
		}
		seen[key] = true
		var raw json.RawMessage
		if decoder.Decode(&raw) != nil {
			return TaskNotificationEvent{}, invalid
		}
		switch key {
		case "version":
			if json.Unmarshal(raw, &event.Version) != nil {
				return TaskNotificationEvent{}, invalid
			}
		case "type":
			if json.Unmarshal(raw, &event.Type) != nil {
				return TaskNotificationEvent{}, invalid
			}
		case "notification_id", "team_id", "recipient_id":
			var text string
			if json.Unmarshal(raw, &text) != nil {
				return TaskNotificationEvent{}, invalid
			}
			id, err := strconv.ParseInt(text, 10, 64)
			if err != nil || id <= 0 || strconv.FormatInt(id, 10) != text {
				return TaskNotificationEvent{}, invalid
			}
			switch key {
			case "notification_id":
				event.NotificationID = id
			case "team_id":
				event.TeamID = id
			case "recipient_id":
				event.RecipientID = id
			}
		default:
			return TaskNotificationEvent{}, invalid
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') || len(seen) != 5 {
		return TaskNotificationEvent{}, invalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || event.Validate() != nil {
		return TaskNotificationEvent{}, invalid
	}
	return event, nil
}
