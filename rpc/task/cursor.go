package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
)

const taskDueWeekMs int64 = 7 * 24 * 60 * 60 * 1000

// This cursor fixes ordering boundaries; identity and current teams are always
// re-resolved independently. It is opaque, not an authorization credential.
type personalTaskCursor struct {
	Version             int   `json:"version"`
	View                int32 `json:"view"`
	TeamID              int64 `json:"team_id"`
	SnapshotNowUnixMs   int64 `json:"snapshot_now_unix_ms"`
	SnapshotUpperTaskID int64 `json:"snapshot_upper_task_id"`
	LastBucket          int   `json:"last_bucket"`
	LastDueAtUnixMs     int64 `json:"last_due_at_unix_ms"`
	LastTaskID          int64 `json:"last_task_id"`
	LastStatus          int32 `json:"last_status"`
}

func decodePersonalTaskCursor(encoded string, view int32, teamID int64) (personalTaskCursor, error) {
	var cursor personalTaskCursor
	invalid := errors.New("invalid task cursor")
	if len(encoded) > 2048 {
		return cursor, invalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != encoded {
		return cursor, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return cursor, invalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return cursor, invalid
	}
	for _, key := range []string{"version", "view", "team_id", "snapshot_now_unix_ms", "snapshot_upper_task_id", "last_bucket", "last_due_at_unix_ms", "last_task_id", "last_status"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return cursor, invalid
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return cursor, invalid
	}
	if cursor.Version != 1 || cursor.View != view || cursor.TeamID != teamID || cursor.SnapshotNowUnixMs <= 0 || cursor.SnapshotNowUnixMs > math.MaxInt64-taskDueWeekMs || cursor.SnapshotUpperTaskID <= 0 || cursor.LastTaskID <= 0 || cursor.LastTaskID > cursor.SnapshotUpperTaskID {
		return cursor, invalid
	}
	if view == 1 {
		if cursor.LastStatus != 2 || cursor.LastBucket != 0 || cursor.LastDueAtUnixMs != 0 {
			return cursor, invalid
		}
		return cursor, nil
	}
	if view != 0 || cursor.LastStatus < 0 || cursor.LastStatus > 1 || cursor.LastBucket < 0 || cursor.LastBucket > 3 {
		return cursor, invalid
	}
	due, now := cursor.LastDueAtUnixMs, cursor.SnapshotNowUnixMs
	switch cursor.LastBucket {
	case 0:
		if due <= 0 || due >= now {
			return cursor, invalid
		}
	case 1:
		if due < now || due > now+taskDueWeekMs {
			return cursor, invalid
		}
	case 2:
		if due <= now+taskDueWeekMs {
			return cursor, invalid
		}
	case 3:
		if due != 0 {
			return cursor, invalid
		}
	}
	return cursor, nil
}

func encodePersonalTaskCursor(cursor personalTaskCursor) string {
	data, _ := json.Marshal(cursor) // All fields are concrete integer values.
	return base64.RawURLEncoding.EncodeToString(data)
}
