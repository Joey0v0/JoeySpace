package model

import (
	"math"
	"testing"
)

func TestBotTaskItemMsgIDsPreserveLegacyAndSeparateItems(t *testing.T) {
	seen := map[string]bool{}
	for index := int32(0); index < 5; index++ {
		id, err := BotTaskItemMsgID(math.MaxInt64, index)
		if err != nil || len(id) > 64 || seen[id] {
			t.Fatalf("index=%d id=%q err=%v", index, id, err)
		}
		seen[id] = true
		if index == 0 && id != "bot-task:9223372036854775807" {
			t.Fatalf("legacy ID changed: %q", id)
		}
		if index == 4 && id != "bot-task:9223372036854775807:4" {
			t.Fatal(id)
		}
	}
	for _, pair := range []struct {
		run   int64
		index int32
	}{{0, 0}, {-1, 0}, {1, -1}, {1, 5}, {1, math.MaxInt32}} {
		if id, err := BotTaskItemMsgID(pair.run, pair.index); err == nil || id != "" {
			t.Fatalf("invalid identity accepted: %+v %q %v", pair, id, err)
		}
	}
}
