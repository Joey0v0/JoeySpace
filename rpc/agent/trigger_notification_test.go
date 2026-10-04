package agent

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
)

func notificationTestMessage(t *testing.T, id int64) kafka.Message {
	t.Helper()
	body, err := json.Marshal(model.AgentTriggerEvent{Version: model.AgentTriggerVersion, Action: model.AgentTriggerAction, MessageID: id})
	if err != nil {
		t.Fatal(err)
	}
	return kafka.Message{Topic: "task_triggers", Partition: 2, Offset: 97,
		Key: []byte((model.AgentTriggerOutbox{MessageID: id}).RequestKey()), Value: body}
}

func TestDecodeTriggerNotificationKeepsExactLargeIDsAndIgnoresHeaders(t *testing.T) {
	for _, id := range []int64{1, 42, 9007199254740993, math.MaxInt64} {
		t.Run(strconv.FormatInt(id, 10), func(t *testing.T) {
			message := notificationTestMessage(t, id)
			message.Headers = []kafka.Header{{Key: "authorization", Value: []byte("Bearer not-an-identity")},
				{Key: "actor_id", Value: []byte("999")}, {Key: "team_id", Value: []byte("888")}}
			got, err := DecodeTriggerNotification(message)
			want := model.AgentTriggerEvent{Version: model.AgentTriggerVersion, Action: model.AgentTriggerAction, MessageID: id}
			if err != nil || got != want {
				t.Fatalf("decoded=%+v want=%+v err=%v", got, want, err)
			}
		})
	}
	message := notificationTestMessage(t, 42)
	message.Value = []byte(" \n { \"message_id\" : \"42\", \"action\" : \"extract_tasks\", \"version\" : 1 } \t")
	if got, err := DecodeTriggerNotification(message); err != nil || got.MessageID != 42 {
		t.Fatalf("valid whitespace/order: %+v %v", got, err)
	}
}

func TestDecodeTriggerNotificationRejectsMalformedShapeAndTypes(t *testing.T) {
	valid := `{"version":1,"action":"extract_tasks","message_id":"42"}`
	for name, body := range map[string]string{
		"empty": "", "null": "null", "array": "[]", "string": `"value"`, "number": "1", "empty object": "{}",
		"missing version":   `{"action":"extract_tasks","message_id":"42"}`,
		"missing action":    `{"version":1,"message_id":"42"}`,
		"missing message":   `{"version":1,"action":"extract_tasks"}`,
		"unknown":           `{"version":1,"action":"extract_tasks","message_id":"42","actor_id":1}`,
		"duplicate version": `{"version":1,"version":1,"action":"extract_tasks","message_id":"42"}`,
		"duplicate action":  `{"version":1,"action":"extract_tasks","action":"extract_tasks","message_id":"42"}`,
		"duplicate id":      `{"version":1,"action":"extract_tasks","message_id":"42","message_id":"42"}`,
		"escaped duplicate": `{"version":1,"vers\u0069on":1,"action":"extract_tasks","message_id":"42"}`,
		"second object":     valid + "{}", "trailing null": valid + " null", "trailing junk": valid + " x",
		"truncated": valid[:len(valid)-1], "trailing comma": valid[:len(valid)-1] + ",}",
	} {
		t.Run(name, func(t *testing.T) {
			message := notificationTestMessage(t, 42)
			message.Value = []byte(body)
			got, err := DecodeTriggerNotification(message)
			if !errors.Is(err, ErrInvalidTriggerNotification) || got != (model.AgentTriggerEvent{}) {
				t.Fatalf("accepted malformed notification: %+v %v", got, err)
			}
		})
	}
	for _, value := range []string{`"1"`, "1.0", "1e0", "0", "2", "-1", "null", "true", "[]", "{}"} {
		message := notificationTestMessage(t, 42)
		message.Value = []byte(strings.Replace(valid, `"version":1`, `"version":`+value, 1))
		if _, err := DecodeTriggerNotification(message); err != ErrInvalidTriggerNotification {
			t.Fatalf("accepted version %s: %v", value, err)
		}
	}
	for _, value := range []string{"null", "1", "true", "[]", "{}", `"other"`, `" extract_tasks"`, `"extract_tasks "`} {
		message := notificationTestMessage(t, 42)
		message.Value = []byte(strings.Replace(valid, `"action":"extract_tasks"`, `"action":`+value, 1))
		if _, err := DecodeTriggerNotification(message); err != ErrInvalidTriggerNotification {
			t.Fatalf("accepted action %s: %v", value, err)
		}
	}
	for _, value := range []string{"null", "42", "42.0", "4.2e1", "true", "[]", "{}", `"0"`, `"-1"`, `"+42"`, `"042"`, `" 42"`, `"42 "`,
		`"4.2e1"`, `"42.0"`, `"9223372036854775808"`, `"４２"`, `"42\u0000"`} {
		message := notificationTestMessage(t, 42)
		message.Value = []byte(strings.Replace(valid, `"message_id":"42"`, `"message_id":`+value, 1))
		if _, err := DecodeTriggerNotification(message); err != ErrInvalidTriggerNotification {
			t.Fatalf("accepted message ID %s: %v", value, err)
		}
	}
}

func TestDecodeTriggerNotificationChecksBodyLimitUTF8AndExactKey(t *testing.T) {
	message := notificationTestMessage(t, 42)
	message.Value = append(message.Value, []byte(strings.Repeat(" ", 4096-len(message.Value)))...)
	if _, err := DecodeTriggerNotification(message); err != nil {
		t.Fatal("rejected exactly 4096-byte valid body", err)
	}
	message.Value = append(message.Value, ' ')
	if _, err := DecodeTriggerNotification(message); err != ErrInvalidTriggerNotification {
		t.Fatal("accepted oversized body", err)
	}
	message = notificationTestMessage(t, 42)
	message.Value = append(message.Value, 0xff)
	if _, err := DecodeTriggerNotification(message); err != ErrInvalidTriggerNotification {
		t.Fatal("accepted invalid UTF-8", err)
	}
	for _, key := range []string{"", "agent-trigger:tasks:43", "agent-trigger:tasks:042", " agent-trigger:tasks:42", "agent-trigger:tasks:42\x00"} {
		message = notificationTestMessage(t, 42)
		message.Key = []byte(key)
		message.Headers = []kafka.Header{{Key: "request_key", Value: []byte("agent-trigger:tasks:42")}}
		if _, err := DecodeTriggerNotification(message); err != ErrInvalidTriggerNotification {
			t.Fatalf("header rescued invalid Kafka key %q: %v", key, err)
		}
	}
}
