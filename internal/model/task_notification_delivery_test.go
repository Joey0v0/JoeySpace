package model

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestDecodeTaskNotificationEventPreservesLargeIDsAndAcceptsExactWireBound(t *testing.T) {
	const wire = `{"version":1,"type":"task_notification_changed","notification_id":"9223372036854775807","team_id":"9007199254740993","recipient_id":"9223372036854775806"}`
	want := TaskNotificationEvent{Version: 1, Type: "task_notification_changed", NotificationID: math.MaxInt64,
		TeamID: 9007199254740993, RecipientID: math.MaxInt64 - 1}
	if TaskNotificationMaxWireBytes != 1024 || TaskNotificationPushPath != "/internal/task-notifications" {
		t.Fatal("changed dedicated notification transport contract")
	}
	for _, input := range []string{wire, "\n" + wire + "\t", wire + strings.Repeat(" ", 1024-len(wire))} {
		got, err := DecodeTaskNotificationEvent([]byte(input))
		if err != nil || got != want {
			t.Fatalf("valid bounded event lost IDs: got=%+v error=%v", got, err)
		}
	}
	if _, err := DecodeTaskNotificationEvent([]byte(wire + strings.Repeat(" ", 1025-len(wire)))); err == nil {
		t.Fatal("wire exceeding 1024 bytes accepted")
	}
	// JSON object order is not part of the event identity.
	reordered := `{"recipient_id":"9223372036854775806","team_id":"9007199254740993","notification_id":"9223372036854775807","type":"task_notification_changed","version":1}`
	if got, err := DecodeTaskNotificationEvent([]byte(reordered)); err != nil || got != want {
		t.Fatalf("reordered fields changed identity: %+v %v", got, err)
	}
}

func TestDecodeTaskNotificationEventRequiresExactlyFiveUniqueFieldsAndOneJSON(t *testing.T) {
	base := map[string]any{"version": 1, "type": "task_notification_changed", "notification_id": "1", "team_id": "2", "recipient_id": "3"}
	encode := func(value any) []byte {
		t.Helper()
		wire, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return wire
	}
	valid := string(encode(base))
	for _, key := range []string{"version", "type", "notification_id", "team_id", "recipient_id"} {
		t.Run("missing "+key, func(t *testing.T) {
			missing := make(map[string]any, 4)
			for name, value := range base {
				if name != key {
					missing[name] = value
				}
			}
			if event, err := DecodeTaskNotificationEvent(encode(missing)); err == nil || event != (TaskNotificationEvent{}) {
				t.Fatal("missing required field produced a usable event")
			}
		})
		t.Run("duplicate "+key, func(t *testing.T) {
			duplicate := strings.TrimSuffix(valid, "}") + `,"` + key + `":` + string(encode(base[key])) + "}"
			if _, err := DecodeTaskNotificationEvent([]byte(duplicate)); err == nil {
				t.Fatal("duplicate field accepted, even when its value agrees")
			}
		})
	}
	for _, wire := range [][]byte{
		nil, []byte(""), []byte("null"), []byte("[]"), []byte(`"event"`),
		[]byte(strings.TrimSuffix(valid, "}") + `,"token":"private-user-token"}`),
		[]byte(valid + " {}"), []byte(valid + " null"), []byte(valid + " trailing"),
		[]byte(valid[:len(valid)-1]), append([]byte(valid), byte(0xff)),
		[]byte(strings.Replace(valid, "task_notification_changed", "task_notification_\xffchanged", 1)),
	} {
		if event, err := DecodeTaskNotificationEvent(wire); err == nil || event != (TaskNotificationEvent{}) {
			t.Fatalf("malformed or nonexclusive event became usable: %q", wire)
		}
	}
}

func TestDecodeTaskNotificationEventRejectsNoncanonicalIDsAndUnsupportedMetadata(t *testing.T) {
	base := map[string]any{"version": 1, "type": "task_notification_changed", "notification_id": "1", "team_id": "2", "recipient_id": "3"}
	for _, key := range []string{"notification_id", "team_id", "recipient_id", "version", "type"} {
		values := []any{nil, true, []string{"1"}, map[string]string{"id": "1"}}
		switch key {
		case "version":
			values = append(values, 0, -1, 2, "1", 1.5)
		case "type":
			values = append(values, "", "chat_messages", "TASK_NOTIFICATION_CHANGED", 1)
		default:
			values = append(values, 1, 0, "", "0", "-1", "+1", "01", " 1", "1 ", "1.0", "1e3", "１", "9223372036854775808")
		}
		for _, value := range values {
			bad := make(map[string]any, 5)
			for name, original := range base {
				bad[name] = original
			}
			bad[key] = value
			wire, err := json.Marshal(bad)
			if err != nil {
				t.Fatal(err)
			}
			if event, err := DecodeTaskNotificationEvent(wire); err == nil || event != (TaskNotificationEvent{}) {
				t.Fatalf("invalid %s=%v produced a usable event", key, value)
			}
		}
	}
}

func TestTaskNotificationDeliveryRequiresOriginalIDAndExplicitOutcome(t *testing.T) {
	for _, outcome := range []string{"queued", "offline", "denied"} {
		delivery := TaskNotificationDelivery{NotificationID: math.MaxInt64, Outcome: outcome}
		if err := delivery.Validate(math.MaxInt64); err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(delivery)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(wire), `"notification_id":"9223372036854775807"`) {
			t.Fatal("delivery echo lost exact string ID")
		}
		var decoded TaskNotificationDelivery
		if err := json.Unmarshal(wire, &decoded); err != nil || decoded != delivery {
			t.Fatalf("delivery round trip changed: %+v %v", decoded, err)
		}
	}
	for _, delivery := range []TaskNotificationDelivery{
		{NotificationID: 1, Outcome: "queued"}, {NotificationID: 0, Outcome: "queued"}, {NotificationID: -1, Outcome: "offline"},
		{NotificationID: math.MaxInt64, Outcome: ""}, {NotificationID: math.MaxInt64, Outcome: "delivered"},
		{NotificationID: math.MaxInt64, Outcome: "accepted"}, {NotificationID: math.MaxInt64, Outcome: "QUEUED"},
	} {
		if err := delivery.Validate(math.MaxInt64); err == nil {
			t.Fatalf("mismatched ID or invented delivery outcome accepted: %+v", delivery)
		}
	}
	for _, expected := range []int64{0, -1} {
		if err := (TaskNotificationDelivery{NotificationID: expected, Outcome: "queued"}).Validate(expected); err == nil {
			t.Fatal("invalid original ID accepted just because response repeats it")
		}
	}
}
