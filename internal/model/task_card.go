package model

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MessageContentTaskCard int8 = 4

// Reserved for IM-generated task results; ordinary WS clients cannot use it.
const BotTaskMsgIDPrefix = "bot-task:"

// TaskCreatedCard is an immutable creation result, not a live task status.
// The enclosing message supplies the group, bot and authorizing user.
type TaskCreatedCard struct {
	Version int    `json:"version"`
	TaskID  string `json:"task_id"`
	Title   string `json:"title"`
}

// EncodeTaskCreatedCard accepts the actual Task result and the confirmed title.
// It neither calls a model nor grants access to the referenced task.
func EncodeTaskCreatedCard(taskID int64, title string) (string, error) {
	card := TaskCreatedCard{Version: 1, TaskID: strconv.FormatInt(taskID, 10), Title: title}
	if err := card.validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(card)
	return string(body), err
}

// DecodeTaskCreatedCard rejects unsupported versions and noncanonical IDs.
// Exact field names prevent the Go and browser decoders interpreting different fields.
func DecodeTaskCreatedCard(content string) (TaskCreatedCard, error) {
	var card TaskCreatedCard
	if len(content) > 4096 || !utf8.ValidString(content) {
		return card, errors.New("invalid task card content")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &fields); err != nil || len(fields) != 3 {
		return card, errors.New("invalid task card fields")
	}
	if err := json.Unmarshal(fields["version"], &card.Version); err != nil {
		return card, errors.New("invalid task card version")
	}
	if err := json.Unmarshal(fields["task_id"], &card.TaskID); err != nil {
		return card, errors.New("invalid task card ID")
	}
	if err := json.Unmarshal(fields["title"], &card.Title); err != nil {
		return card, errors.New("invalid task card title")
	}
	if err := card.validate(); err != nil {
		return TaskCreatedCard{}, err
	}
	return card, nil
}

func (c TaskCreatedCard) validate() error {
	id, err := strconv.ParseInt(c.TaskID, 10, 64)
	if c.Version != 1 || err != nil || id <= 0 || strconv.FormatInt(id, 10) != c.TaskID ||
		!utf8.ValidString(c.Title) || strings.TrimSpace(c.Title) != c.Title ||
		utf8.RuneCountInString(c.Title) < 1 || utf8.RuneCountInString(c.Title) > 200 {
		return errors.New("invalid or unsupported task creation card")
	}
	return nil
}
