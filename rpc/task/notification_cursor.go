package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
)

type notificationCursor struct {
	Version          int    `json:"v"`
	TeamID           int64  `json:"team"`
	UpperID          int64  `json:"upper"`
	ReadCutoffUnixMs int64  `json:"read_cutoff"`
	Phase            string `json:"phase"`
	LastID           int64  `json:"last"`
}

func encodeNotificationCursor(cursor notificationCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeNotificationCursor(value string, teamID int64) (notificationCursor, error) {
	var cursor notificationCursor
	if value == "" || len(value) > 2048 {
		return cursor, errors.New("invalid cursor")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(value)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err != nil || decoder.Decode(&cursor) != nil || decoder.Decode(&struct{}{}) != io.EOF || encodeNotificationCursor(cursor) != value || cursor.Version != 1 || cursor.TeamID != teamID || cursor.UpperID <= 0 || cursor.ReadCutoffUnixMs <= 0 || cursor.ReadCutoffUnixMs > maxTaskDueAtUnixMs || cursor.LastID <= 0 || cursor.LastID > cursor.UpperID || (cursor.Phase != "unread" && cursor.Phase != "read") {
		return notificationCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}
