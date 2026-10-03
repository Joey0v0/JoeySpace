package main

import (
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/rpc/agent/pb"
)

type agentDraftDeadline struct {
	Text                       string `json:"text"`
	Source                     string `json:"source"`
	SourceMessageID            int64  `json:"source_message_id,string"`
	ReferenceUnixMs            int64  `json:"reference_unix_ms"`
	Timezone                   string `json:"timezone"`
	Resolution                 string `json:"resolution"`
	Reason                     string `json:"reason"`
	ParsedUnixMs               int64  `json:"parsed_unix_ms"`
	InstructionReferenceUnixMs int64  `json:"instruction_reference_unix_ms"`
}

func draftDeadlineMetadataResponse(meta *pb.TaskDraftDeadline) *agentDraftDeadline {
	if meta == nil {
		return nil
	}
	return &agentDraftDeadline{
		Text: meta.GetText(), Source: meta.GetSource(), SourceMessageID: meta.GetSourceMessageId(), ReferenceUnixMs: meta.GetReferenceUnixMs(),
		Timezone: meta.GetTimezone(), Resolution: meta.GetResolution(), Reason: meta.GetReason(), ParsedUnixMs: meta.GetParsedUnixMs(),
		InstructionReferenceUnixMs: meta.GetInstructionReferenceUnixMs(),
	}
}

func validDeadlineResolution(value string) bool {
	switch value {
	case "", "none", "parsed", "needs_input", "selected", "unset":
		return true
	default:
		return false
	}
}

func validDeadlineReason(value string) bool {
	switch value {
	case "", "unsupported_expression", "invalid_time", "invalid_date", "nonexistent_local_time", "ambiguous_local_time", "out_of_range", "missing_reference":
		return true
	default:
		return false
	}
}

func validDraftDeadlineMetadata(draft *pb.TaskDraftItem) bool {
	if draft == nil {
		return false
	}
	meta := draft.GetDeadline()
	if meta == nil {
		return true
	} // A missing message preserves legacy due/review behavior.
	if meta.GetTimezone() != "Asia/Shanghai" || !utf8.ValidString(meta.GetText()) || utf8.RuneCountInString(meta.GetText()) > 200 ||
		strings.TrimSpace(meta.GetText()) != meta.GetText() || meta.GetSourceMessageId() < 0 || !validDeadlineReason(meta.GetReason()) {
		return false
	}
	for _, value := range []int64{meta.GetReferenceUnixMs(), meta.GetParsedUnixMs(), meta.GetInstructionReferenceUnixMs(), draft.GetDueAtUnixMs()} {
		if value < 0 || value > maxDraftDeadlineUnixMs {
			return false
		}
	}
	expression := false
	switch meta.GetSource() {
	case "none":
		if meta.GetText() != "" || meta.GetSourceMessageId() != 0 || meta.GetReferenceUnixMs() != 0 || meta.GetParsedUnixMs() != 0 || meta.GetReason() != "" {
			return false
		}
	case "instruction":
		expression = true
		if meta.GetText() == "" || meta.GetSourceMessageId() != 0 || meta.GetReferenceUnixMs() != meta.GetInstructionReferenceUnixMs() {
			return false
		}
	case "message":
		expression = true
		if meta.GetText() == "" || meta.GetSourceMessageId() <= 0 || meta.GetReferenceUnixMs() <= 0 {
			return false
		}
	default:
		return false
	}
	if expression && !((meta.GetParsedUnixMs() > 0 && meta.GetReason() == "") || (meta.GetParsedUnixMs() == 0 && meta.GetReason() != "")) {
		return false
	}
	if meta.GetReason() == "missing_reference" && (meta.GetSource() != "instruction" || meta.GetReferenceUnixMs() != 0) {
		return false
	}
	switch meta.GetResolution() {
	case "none":
		return !expression && draft.GetDueAtUnixMs() == 0
	case "parsed":
		return expression && meta.GetParsedUnixMs() > 0 && draft.GetDueAtUnixMs() == meta.GetParsedUnixMs()
	case "needs_input":
		return expression && meta.GetParsedUnixMs() == 0 && meta.GetReason() != "" && draft.GetDueAtUnixMs() == 0
	case "selected":
		return draft.GetDueAtUnixMs() > 0
	case "unset":
		return draft.GetDueAtUnixMs() == 0
	default:
		return false
	}
}
