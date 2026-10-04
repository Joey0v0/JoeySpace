package model

import (
	"strings"
	"testing"
)

func TestAgentTriggerMemberNameKeepsExactUnicodeAndBounds(t *testing.T) {
	for _, name := range []string{"张三", "Alice", "ALICE", "é", strings.Repeat("张", 64)} {
		if !ValidAgentTriggerMemberName(name) {
			t.Errorf("valid literal rejected %q", name)
		}
	}
	for _, name := range []string{"", " 张三", "张三 ", "\xff", strings.Repeat("张", 65)} {
		if ValidAgentTriggerMemberName(name) {
			t.Errorf("invalid literal accepted %q", name)
		}
	}
	if ValidAgentTriggerMemberCandidate(77, "Alice", "", "alice") || ValidAgentTriggerMemberCandidate(77, "e", "", "é") {
		t.Fatal("shape validation normalized name instead of exact match")
	}
}

func TestAgentTriggerMemberCandidateRequiresBoundedMatchingFields(t *testing.T) {
	for _, candidate := range []struct {
		id                 int64
		username, nickname string
		valid              bool
	}{
		{77, "张三", "", true}, {77, "zhangsan", "张三", true}, {77, "张三", "张三", true},
		{0, "张三", "", false}, {-1, "张三", "", false}, {77, "", "张三", false},
		{77, "zhangsan", "lisi", false}, {77, "\xff", "张三", false}, {77, "张三", "\xff", false},
		{77, strings.Repeat("x", 65), "张三", false}, {77, "张三", strings.Repeat("张", 65), false},
	} {
		if got := ValidAgentTriggerMemberCandidate(candidate.id, candidate.username, candidate.nickname, "张三"); got != candidate.valid {
			t.Errorf("candidate id=%d valid=%v wanted=%v", candidate.id, got, candidate.valid)
		}
	}
}
