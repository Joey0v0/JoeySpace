package model

import (
	"strings"
	"unicode/utf8"
)

const AgentTriggerMemberResponseLimit = 32 * 1024
const AgentTriggerMemberCandidateLimit = 20

// Service requests carry the already-trimmed literal, not a fuzzy identifier.
func ValidAgentTriggerMemberName(name string) bool {
	return utf8.ValidString(name) && name == strings.TrimSpace(name) && utf8.RuneCountInString(name) >= 1 && utf8.RuneCountInString(name) <= 64
}

// Each trust boundary independently checks order/uniqueness/count/scope. This
// helper only checks one candidate's ID, bounded UTF-8 fields and exact match.
func ValidAgentTriggerMemberCandidate(id int64, username, nickname, name string) bool {
	return id > 0 && ValidAgentTriggerMemberName(name) &&
		utf8.ValidString(username) && utf8.ValidString(nickname) &&
		utf8.RuneCountInString(username) >= 1 && utf8.RuneCountInString(username) <= 64 &&
		utf8.RuneCountInString(nickname) <= 64 && (username == name || nickname == name)
}
