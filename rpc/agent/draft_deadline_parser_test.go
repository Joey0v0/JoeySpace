package agent

import (
	"reflect"
	"strings"
	"testing"
	"time"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func deadlineTestUTC(t *testing.T, value string) int64 {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.UnixMilli()
}

func TestInterpretDraftDeadlineAbsoluteRelativeAndPrecision(t *testing.T) {
	for _, tc := range []struct{ text, reference, expected string }{
		{"2026-10-03 15:30", "", "2026-10-03T07:30:00Z"},
		{"2026/10/03 15:30:45.1", "2026-01-01T00:00:00Z", "2026-10-03T07:30:45.1Z"},
		{"2026年1月2日 00:01:02.12", "", "2026-01-01T16:01:02.12Z"},
		{"2024-02-29 23:59:59.123", "", "2024-02-29T15:59:59.123Z"},
		{"今天 00:05", "2026-10-03T15:59:59Z", "2026-10-02T16:05:00Z"},
		{"明天 00:05", "2026-10-03T16:00:00Z", "2026-10-04T16:05:00Z"},
		{"明天 09:00", "2026-12-31T04:00:00Z", "2027-01-01T01:00:00Z"},
		{"后天 09:00:01.001", "2024-02-28T04:00:00Z", "2024-03-01T01:00:01.001Z"},
		{"明天 12:00", "1986-05-03T04:00:00Z", "1986-05-04T03:00:00Z"},
		{"明天 12:00", "1986-09-13T03:00:00Z", "1986-09-14T04:00:00Z"},
		{"1970-01-01 08:00:00.001", "", "1970-01-01T00:00:00.001Z"},
		{"9999-12-31 23:59:59.999", "", "9999-12-31T15:59:59.999Z"},
	} {
		t.Run(tc.text+tc.reference, func(t *testing.T) {
			var ref *int64
			if tc.reference != "" {
				value := deadlineTestUTC(t, tc.reference)
				ref = &value
			}
			evidence := draftDeadlineEvidence{Text: tc.text, Source: "instruction"}
			got, err := interpretDraftDeadline("请在"+tc.text+"完成", nil, evidence, ref)
			if err != nil || got.Resolution != "parsed" || got.DueAtUnixMs != deadlineTestUTC(t, tc.expected) || got.Text != tc.text || got.Source != "instruction" || got.Timezone != draftDeadlineTimezone || got.Reason != "" {
				t.Fatalf("parsed: %+v %v", got, err)
			}
			if ref == nil && got.ReferenceUnixMs != 0 || ref != nil && got.ReferenceUnixMs != *ref {
				t.Fatalf("reference: %+v", got)
			}
		})
	}
}

func TestInterpretDraftDeadlineNeedsInputNeverInventsTime(t *testing.T) {
	ref := deadlineTestUTC(t, "2026-10-03T04:00:00Z")
	for _, tc := range []struct{ text, reason string }{
		{"明天下午", "unsupported_expression"}, {"下周 09:00", "unsupported_expression"},
		{"月底", "unsupported_expression"}, {"2026-10-03", "unsupported_expression"}, {"09:00", "unsupported_expression"},
		{"10月3日 09:00", "unsupported_expression"}, {"2026-10-03T09:00Z", "unsupported_expression"},
		{"2026-10-03 9:00", "unsupported_expression"}, {"2026-10-03 09:00:01.1234", "unsupported_expression"},
		{"2026-02-29 09:00", "invalid_date"}, {"2024-02-30 09:00", "invalid_date"},
		{"2026-13-01 09:00", "invalid_date"}, {"2026-00-01 09:00", "invalid_date"},
		{"2026-01-00 09:00", "invalid_date"}, {"0000-01-01 09:00", "invalid_date"},
		{"2026-10-03 24:00", "invalid_time"}, {"2026-10-03 09:60", "invalid_time"}, {"2026-10-03 09:00:60", "invalid_time"},
		{"1970-01-01 08:00", "out_of_range"}, {"1969-12-31 09:00", "out_of_range"},
		{"1986-05-04 02:30", "nonexistent_local_time"}, {"1986-09-14 01:30", "ambiguous_local_time"},
		{"1991-04-14 02:30:01.001", "nonexistent_local_time"}, {"1991-09-15 01:30:01.001", "ambiguous_local_time"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := interpretDraftDeadline(tc.text, nil, draftDeadlineEvidence{Text: tc.text, Source: "instruction"}, &ref)
			if err != nil || got.Resolution != "needs_input" || got.DueAtUnixMs != 0 || got.Reason != tc.reason {
				t.Fatalf("needs input: %+v %v", got, err)
			}
		})
	}
	got, err := interpretDraftDeadline("明天 09:00", nil, draftDeadlineEvidence{Text: "明天 09:00", Source: "instruction"}, nil)
	if err != nil || got.Reason != "missing_reference" || got.DueAtUnixMs != 0 {
		t.Fatalf("missing ref: %+v %v", got, err)
	}
}

func TestInterpretDraftDeadlineNoneRequiresCompleteEmptyEvidence(t *testing.T) {
	got, err := interpretDraftDeadline("完成任务", nil, draftDeadlineEvidence{Source: "none"}, nil)
	if err != nil || got.Resolution != "none" || got.DueAtUnixMs != 0 || got.ReferenceUnixMs != 0 || got.Timezone != draftDeadlineTimezone {
		t.Fatalf("none: %+v %v", got, err)
	}
}

func TestInterpretDraftDeadlineMessageUsesExactAuthorizedSourceTime(t *testing.T) {
	const messageID = int64(9007199254740993)
	messageRef := deadlineTestUTC(t, "2026-10-01T04:00:00Z")
	instructionRef := deadlineTestUTC(t, "2026-10-03T04:00:00Z")
	messages := []*impb.TeamGroupMessage{{Id: messageID - 1, ContentType: 1, Content: "明天 09:00", CreatedAtUnixMs: instructionRef}, {Id: messageID, ContentType: 1, Content: "在明天 09:00交付", CreatedAtUnixMs: messageRef}}
	got, err := interpretDraftDeadline("明天 09:00", messages, draftDeadlineEvidence{Text: "明天 09:00", Source: "message", SourceMessageID: messageID}, &instructionRef)
	if err != nil || got.DueAtUnixMs != deadlineTestUTC(t, "2026-10-02T01:00:00Z") || got.ReferenceUnixMs != messageRef || got.SourceMessageID != messageID {
		t.Fatalf("source: %+v %v", got, err)
	}
	for _, replacement := range []int64{instructionRef, instructionRef + 24*60*60*1000} {
		replayed, err := interpretDraftDeadline("new instruction", messages, draftDeadlineEvidence{Text: "明天 09:00", Source: "message", SourceMessageID: messageID}, &replacement)
		if err != nil || replayed != got {
			t.Fatalf("message result drifted: %+v %v", replayed, err)
		}
	}
}

func TestInterpretDraftDeadlineRejectsUnverifiableEvidenceAndReference(t *testing.T) {
	ref := deadlineTestUTC(t, "2026-10-03T04:00:00Z")
	message := &impb.TeamGroupMessage{Id: 99, ContentType: 1, Content: "明天 09:00", CreatedAtUnixMs: ref}
	for _, tc := range []struct {
		name      string
		evidence  draftDeadlineEvidence
		messages  []*impb.TeamGroupMessage
		reference *int64
	}{
		{"missing source", draftDeadlineEvidence{}, nil, nil},
		{"empty instruction", draftDeadlineEvidence{Source: "instruction"}, nil, nil},
		{"empty message", draftDeadlineEvidence{Source: "none", SourceMessageID: 99}, nil, nil},
		{"none with text", draftDeadlineEvidence{Source: "none", Text: "明天 09:00"}, nil, nil},
		{"invented text", draftDeadlineEvidence{Source: "instruction", Text: "后天 09:00"}, nil, nil},
		{"trimmed evidence", draftDeadlineEvidence{Source: "instruction", Text: " 明天 09:00"}, nil, nil},
		{"too long", draftDeadlineEvidence{Source: "instruction", Text: strings.Repeat("明", 201)}, nil, nil},
		{"bad utf8", draftDeadlineEvidence{Source: "instruction", Text: "\xff"}, nil, nil},
		{"unknown source", draftDeadlineEvidence{Source: "other", Text: "明天 09:00"}, nil, nil},
		{"negative ID", draftDeadlineEvidence{Source: "instruction", Text: "明天 09:00", SourceMessageID: -1}, nil, nil},
		{"instruction with ID", draftDeadlineEvidence{Source: "instruction", Text: "明天 09:00", SourceMessageID: 99}, nil, nil},
		{"zero message ID", draftDeadlineEvidence{Source: "message", Text: "明天 09:00"}, []*impb.TeamGroupMessage{message}, nil},
		{"unauthorized message", draftDeadlineEvidence{Source: "message", Text: "明天 09:00", SourceMessageID: 100}, []*impb.TeamGroupMessage{message}, nil},
		{"duplicate ID", draftDeadlineEvidence{Source: "message", Text: "明天 09:00", SourceMessageID: 99}, []*impb.TeamGroupMessage{message, message}, nil},
		{"non-text", draftDeadlineEvidence{Source: "message", Text: "明天 09:00", SourceMessageID: 99}, []*impb.TeamGroupMessage{{Id: 99, ContentType: 2, Content: message.Content, CreatedAtUnixMs: ref}}, nil},
		{"missing message time", draftDeadlineEvidence{Source: "message", Text: "明天 09:00", SourceMessageID: 99}, []*impb.TeamGroupMessage{{Id: 99, ContentType: 1, Content: message.Content}}, nil},
		{"message invalid time", draftDeadlineEvidence{Source: "message", Text: "明天 09:00", SourceMessageID: 99}, []*impb.TeamGroupMessage{{Id: 99, ContentType: 1, Content: message.Content, CreatedAtUnixMs: maxDraftDueAtUnixMs + 1}}, nil},
		{"zero reference", draftDeadlineEvidence{Source: "instruction", Text: "明天 09:00"}, nil, draftID(0)},
		{"negative reference", draftDeadlineEvidence{Source: "instruction", Text: "明天 09:00"}, nil, draftID(-1)},
		{"overflow reference", draftDeadlineEvidence{Source: "instruction", Text: "明天 09:00"}, nil, draftID(maxDraftDueAtUnixMs + 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := interpretDraftDeadline("明天 09:00", tc.messages, tc.evidence, tc.reference)
			if status.Code(err) != codes.FailedPrecondition || !reflect.DeepEqual(got, draftDeadlineInterpretation{}) {
				t.Fatalf("bad evidence: %+v %v", got, err)
			}
		})
	}
}

func TestInterpretDraftDeadlineFixedInstructionReferenceRemainsStable(t *testing.T) {
	ref := deadlineTestUTC(t, "2026-10-03T15:59:59.999Z")
	evidence := draftDeadlineEvidence{Text: "明天 09:00", Source: "instruction"}
	first, err := interpretDraftDeadline(evidence.Text, nil, evidence, &ref)
	if err != nil {
		t.Fatal(err)
	}
	second, err := interpretDraftDeadline(evidence.Text, nil, evidence, &ref)
	if err != nil || second != first || first.DueAtUnixMs != deadlineTestUTC(t, "2026-10-04T01:00:00Z") {
		t.Fatalf("replay: %+v %+v %v", first, second, err)
	}
	// A different supplied reference is deliberately a different interpretation;
	// request idempotency must prevent reusing the original key with this input.
	later := ref + 1
	third, err := interpretDraftDeadline(evidence.Text, nil, evidence, &later)
	if err != nil || third.DueAtUnixMs != deadlineTestUTC(t, "2026-10-05T01:00:00Z") {
		t.Fatalf("new intent: %+v %v", third, err)
	}
}
