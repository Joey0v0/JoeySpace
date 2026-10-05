package model

import (
	"encoding/json"
	"math"
	"testing"
)

func TestTaskNotificationEventWireHasOnlyStableStringReferences(t *testing.T) {
	event := TaskNotificationEvent{Version: 1, Type: "task_notification_changed", NotificationID: math.MaxInt64,
		TeamID: 9007199254740993, RecipientID: math.MaxInt64 - 1}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"version": `1`, "type": `"task_notification_changed"`,
		"notification_id": `"9223372036854775807"`, "team_id": `"9007199254740993"`, "recipient_id": `"9223372036854775806"`}
	if len(fields) != len(expected) {
		t.Fatalf("event must contain only five fixed references: %s", wire)
	}
	for name, value := range expected {
		if string(fields[name]) != value {
			t.Fatalf("wire field %s = %s, want %s", name, fields[name], value)
		}
	}
	var decoded TaskNotificationEvent
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != event {
		t.Fatalf("large ID changed during round trip: got %+v, want %+v", decoded, event)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestTaskNotificationEventRejectsUnsupportedOrInvalidReferences(t *testing.T) {
	base := TaskNotificationEvent{Version: 1, Type: "task_notification_changed", NotificationID: 1, TeamID: 2, RecipientID: 3}
	cases := []struct {
		name   string
		mutate func(*TaskNotificationEvent)
	}{
		{"missing version", func(e *TaskNotificationEvent) { e.Version = 0 }},
		{"negative version", func(e *TaskNotificationEvent) { e.Version = -1 }},
		{"unknown version", func(e *TaskNotificationEvent) { e.Version = 2 }},
		{"missing type", func(e *TaskNotificationEvent) { e.Type = "" }},
		{"another event type", func(e *TaskNotificationEvent) { e.Type = "chat_messages" }},
		{"wrong type case", func(e *TaskNotificationEvent) { e.Type = "TASK_NOTIFICATION_CHANGED" }},
		{"zero notification", func(e *TaskNotificationEvent) { e.NotificationID = 0 }},
		{"negative notification", func(e *TaskNotificationEvent) { e.NotificationID = -1 }},
		{"zero team", func(e *TaskNotificationEvent) { e.TeamID = 0 }},
		{"negative team", func(e *TaskNotificationEvent) { e.TeamID = -1 }},
		{"zero recipient", func(e *TaskNotificationEvent) { e.RecipientID = 0 }},
		{"negative recipient", func(e *TaskNotificationEvent) { e.RecipientID = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := base
			tc.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatalf("accepted invalid event %+v", event)
			}
		})
	}
}

func TestTaskNotificationEventJSONRejectsNumericOrOutOfRangeIDs(t *testing.T) {
	for _, wire := range []string{
		`{"version":1,"type":"task_notification_changed","notification_id":9007199254740993,"team_id":"2","recipient_id":"3"}`,
		`{"version":1,"type":"task_notification_changed","notification_id":"1","team_id":2,"recipient_id":"3"}`,
		`{"version":1,"type":"task_notification_changed","notification_id":"1","team_id":"2","recipient_id":3}`,
		`{"version":1,"type":"task_notification_changed","notification_id":"9223372036854775808","team_id":"2","recipient_id":"3"}`,
		`{"version":1,"type":"task_notification_changed","notification_id":"1.5","team_id":"2","recipient_id":"3"}`,
	} {
		var event TaskNotificationEvent
		if err := json.Unmarshal([]byte(wire), &event); err == nil {
			t.Fatalf("accepted non-string or invalid int64 ID: %s", wire)
		}
	}
}

func TestTaskNotificationEventKeyUsesOnlyGlobalNotificationID(t *testing.T) {
	event := TaskNotificationEvent{Version: 1, Type: "task_notification_changed", NotificationID: math.MaxInt64, TeamID: 2, RecipientID: 3}
	want := "task-notification:9223372036854775807"
	if event.Key() != want {
		t.Fatalf("unstable notification key %q", event.Key())
	}
	// Retrying or changing routing metadata cannot invent a different event ID.
	event.TeamID, event.RecipientID = 4, 5
	if event.Key() != want {
		t.Fatalf("routing metadata entered event key: %q", event.Key())
	}
}
