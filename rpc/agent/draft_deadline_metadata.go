package agent

import (
	"strings"
	"unicode/utf8"

	"github.com/yjydist/go-im/rpc/agent/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The original interpretation evidence remains immutable after human edits.
// A comparable value keeps all draft optimistic-lock comparisons comprehensive.
type draftDeadlineMetadata struct {
	Text                       string
	Source                     string
	SourceMessageID            int64
	ReferenceUnixMs            int64
	Timezone                   string
	Resolution                 string
	Reason                     string
	ParsedUnixMs               int64
	InstructionReferenceUnixMs int64
}

func validDeadlineResolution(value string) bool {
	switch value {
	case "", "none", "parsed", "needs_input", "selected", "unset":
		return true
	}
	return false
}

func (m draftDeadlineMetadata) valid(due int64) bool {
	validTime := func(value int64) bool { return value >= 0 && value <= maxDraftDueAtUnixMs }
	if !validTime(due) {
		return false
	}
	if m == (draftDeadlineMetadata{}) {
		return true
	}
	if m.Timezone != draftDeadlineTimezone || !utf8.ValidString(m.Text) || utf8.RuneCountInString(m.Text) > 200 || strings.TrimSpace(m.Text) != m.Text || m.SourceMessageID < 0 || !validTime(m.ReferenceUnixMs) || !validTime(m.ParsedUnixMs) || !validTime(m.InstructionReferenceUnixMs) {
		return false
	}
	validReason := false
	switch m.Reason {
	case "unsupported_expression", "invalid_time", "invalid_date", "nonexistent_local_time", "ambiguous_local_time", "out_of_range", "missing_reference":
		validReason = true
	}
	expression := m.Source == "instruction" || m.Source == "message"
	switch m.Source {
	case "none":
		if m.Text != "" || m.SourceMessageID != 0 || m.ReferenceUnixMs != 0 || m.ParsedUnixMs != 0 || m.Reason != "" {
			return false
		}
	case "instruction":
		if m.Text == "" || m.SourceMessageID != 0 || m.ReferenceUnixMs != m.InstructionReferenceUnixMs {
			return false
		}
	case "message":
		if m.Text == "" || m.SourceMessageID <= 0 || m.ReferenceUnixMs <= 0 {
			return false
		}
	default:
		return false
	}
	if expression && !((m.ParsedUnixMs > 0 && m.Reason == "") || (m.ParsedUnixMs == 0 && validReason)) {
		return false
	}
	if m.Reason == "missing_reference" && (m.Source != "instruction" || m.ReferenceUnixMs != 0) {
		return false
	}
	switch m.Resolution {
	case "none":
		return m.Source == "none" && due == 0
	case "parsed":
		return expression && m.ParsedUnixMs > 0 && due == m.ParsedUnixMs
	case "needs_input":
		return expression && m.ParsedUnixMs == 0 && validReason && due == 0
	case "selected":
		return due > 0
	case "unset":
		return due == 0
	default:
		return false
	}
}

func (m draftDeadlineMetadata) rpc() *pb.TaskDraftDeadline {
	if m == (draftDeadlineMetadata{}) {
		return nil
	}
	return &pb.TaskDraftDeadline{Text: m.Text, Source: m.Source, SourceMessageId: m.SourceMessageID, ReferenceUnixMs: m.ReferenceUnixMs, Timezone: m.Timezone, Resolution: m.Resolution, Reason: m.Reason, ParsedUnixMs: m.ParsedUnixMs, InstructionReferenceUnixMs: m.InstructionReferenceUnixMs}
}

func (d taskDraft) requireDeadlineStateReview(reviewed *int64, resolution string) error {
	if !d.Deadline.valid(d.DueAtUnixMs) {
		return status.Error(codes.Unavailable, "stored deadline metadata is invalid")
	}
	if d.Deadline == (draftDeadlineMetadata{}) {
		if resolution != "" {
			return status.Error(codes.Aborted, "draft deadline state changed; reload before confirming")
		}
		return d.requireDeadlineReview(reviewed)
	}
	if resolution == "" || reviewed == nil {
		return status.Error(codes.FailedPrecondition, "deadline state and value review are required")
	}
	if resolution != d.Deadline.Resolution {
		return status.Error(codes.Aborted, "draft deadline state changed; reload before confirming")
	}
	if err := d.requireDeadlineReview(reviewed); err != nil {
		return err
	}
	if d.Deadline.Resolution == "needs_input" {
		return status.Error(codes.FailedPrecondition, "complete or explicitly clear the deadline before confirming")
	}
	return nil
}
