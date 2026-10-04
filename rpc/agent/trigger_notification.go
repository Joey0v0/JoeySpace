package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/segmentio/kafka-go"
	"github.com/yjydist/go-im/internal/model"
)

// DecodeTriggerNotification accepts notification facts only. Headers confer no
// user identity, and the original actor/scope must later be read from IM.
func DecodeTriggerNotification(message kafka.Message) (model.AgentTriggerEvent, error) {
	var event model.AgentTriggerEvent
	invalid := func() (model.AgentTriggerEvent, error) {
		return model.AgentTriggerEvent{}, ErrInvalidTriggerNotification
	}
	if len(message.Value) == 0 || len(message.Value) > 4096 || !utf8.Valid(message.Value) {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(message.Value))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return invalid()
	}
	seen := make(map[string]bool, 3)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return invalid()
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return invalid()
		}
		switch key {
		case "version":
			if !bytes.Equal(value, []byte("1")) {
				return invalid()
			}
			event.Version = 1
		case "action":
			if err := json.Unmarshal(value, &event.Action); err != nil {
				return invalid()
			}
		case "message_id":
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return invalid()
			}
			id, err := strconv.ParseInt(text, 10, 64)
			if err != nil || id <= 0 || strconv.FormatInt(id, 10) != text {
				return invalid()
			}
			event.MessageID = id
		default:
			return invalid()
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 3 {
		return invalid()
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF || validateTriggerNotification(event) != nil ||
		!bytes.Equal(message.Key, []byte(triggerNotificationKey(event))) {
		return invalid()
	}
	return event, nil
}
