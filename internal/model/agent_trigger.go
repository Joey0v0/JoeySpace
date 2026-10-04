package model

import (
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const AgentTriggerAction = "extract_tasks"
const AgentTriggerVersion = 1

// ParseAgentTaskCommand accepts only a standalone command at the start of text.
func ParseAgentTaskCommand(text string) (string, bool) {
	if !utf8.ValidString(text) {
		return "", false
	}
	rest := strings.TrimSpace(text)
	for _, token := range []string{"@AI", "整理任务"} {
		if !strings.HasPrefix(rest, token) {
			return "", false
		}
		rest = strings.TrimPrefix(rest, token)
		r, _ := utf8.DecodeRuneInString(rest)
		if !unicode.IsSpace(r) {
			return "", false
		}
		rest = strings.TrimSpace(rest)
	}
	if rest == "" || utf8.RuneCountInString(rest) > 2000 {
		return "", false
	}
	return rest, true
}

func AgentTaskCommand(msg *Message) (string, bool) {
	if msg == nil || (msg.SenderType != 0 && msg.SenderType != MessageSenderUser) || msg.InitiatorID != 0 || msg.FromID <= 0 || msg.ToID <= 0 || msg.ChatType != 2 || msg.ContentType != 1 {
		return "", false
	}
	return ParseAgentTaskCommand(msg.Content)
}

// AgentTriggerOutbox belongs to IM. Published means Kafka acknowledged, not Agent completed.
type AgentTriggerOutbox struct {
	MessageID       int64 `gorm:"primaryKey;autoIncrement:false"`
	Action          string
	EventVersion    int
	MsgID           string
	ActorID         int64
	TeamID          int64
	GroupID         int64
	Instruction     string
	ReferenceTimeMS int64
	Published       bool
}

func (AgentTriggerOutbox) TableName() string { return "im_agent_trigger_outbox" }

func (r AgentTriggerOutbox) Validate() error {
	if r.MessageID <= 0 || r.ActorID <= 0 || r.TeamID <= 0 || r.GroupID <= 0 || r.ReferenceTimeMS <= 0 || r.EventVersion != AgentTriggerVersion || r.Action != AgentTriggerAction || r.MsgID == "" || len(r.MsgID) > 64 || !utf8.ValidString(r.MsgID) || strings.ContainsAny(r.MsgID, "\x00\r\n") || r.Instruction == "" || r.Instruction != strings.TrimSpace(r.Instruction) || !utf8.ValidString(r.Instruction) || utf8.RuneCountInString(r.Instruction) > 2000 {
		return errors.New("invalid persisted agent trigger")
	}
	return nil
}

// AgentTriggerEvent is a notification, never a user authorization or source body.
type AgentTriggerEvent struct {
	Version   int    `json:"version"`
	Action    string `json:"action"`
	MessageID int64  `json:"message_id,string"`
}

func (r AgentTriggerOutbox) Event() AgentTriggerEvent {
	return AgentTriggerEvent{Version: r.EventVersion, Action: r.Action, MessageID: r.MessageID}
}

func (r AgentTriggerOutbox) RequestKey() string {
	return "agent-trigger:tasks:" + strconv.FormatInt(r.MessageID, 10)
}
