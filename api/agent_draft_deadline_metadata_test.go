package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/agent/pb"
)

func gatewayParsedDeadline() *pb.TaskDraftItem {
	return &pb.TaskDraftItem{Title: "Task", Revision: 1, DueAtUnixMs: 1791097200123, Deadline: &pb.TaskDraftDeadline{
		Text: "明天 15:00:00.123", Source: "message", SourceMessageId: 9007199254740993, ReferenceUnixMs: 1791010800000,
		Timezone: "Asia/Shanghai", Resolution: "parsed", ParsedUnixMs: 1791097200123, InstructionReferenceUnixMs: 1791010800123,
	}}
}

func TestDraftDeadlineMetadataHTTPPreservesAllFieldsAndLegacyOmission(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		result := deadlineResponse(1791097200123, 1)
		if !legacy {
			result.Draft = gatewayParsedDeadline()
		}
		w := httptest.NewRecorder()
		writeTaskDraftResult(w, 1, result)
		if w.Code != 200 {
			t.Fatalf("response %d %s", w.Code, w.Body.String())
		}
		var body struct {
			Data struct {
				Draft map[string]json.RawMessage `json:"draft"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if legacy {
			if body.Data.Draft["deadline"] != nil {
				t.Fatal("legacy metadata invented")
			}
			continue
		}
		var meta map[string]any
		if err := json.Unmarshal(body.Data.Draft["deadline"], &meta); err != nil {
			t.Fatal(err)
		}
		if len(meta) != 9 || meta["source_message_id"] != "9007199254740993" || meta["parsed_unix_ms"] != float64(1791097200123) ||
			meta["reference_unix_ms"] != float64(1791010800000) || meta["instruction_reference_unix_ms"] != float64(1791010800123) || meta["reason"] != "" {
			t.Fatalf("incomplete or rounded metadata: %v", meta)
		}
	}
}

func TestDraftDeadlineMetadataAcceptsExactContractShapes(t *testing.T) {
	for _, mutate := range []func(*pb.TaskDraftItem){
		func(d *pb.TaskDraftItem) {},
		func(d *pb.TaskDraftItem) {
			d.Deadline.Source = "instruction"
			d.Deadline.SourceMessageId = 0
			d.Deadline.ReferenceUnixMs = d.Deadline.InstructionReferenceUnixMs
		},
		func(d *pb.TaskDraftItem) {
			d.Deadline.Source = "instruction"
			d.Deadline.SourceMessageId = 0
			d.Deadline.ReferenceUnixMs = 0
			d.Deadline.InstructionReferenceUnixMs = 0
			d.Deadline.Text = "2026-10-04 15:00"
		},
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline.ParsedUnixMs = 0
			d.Deadline.Reason = "unsupported_expression"
			d.Deadline.Resolution = "needs_input"
		},
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline.Source = "instruction"
			d.Deadline.SourceMessageId = 0
			d.Deadline.ReferenceUnixMs = 0
			d.Deadline.InstructionReferenceUnixMs = 0
			d.Deadline.ParsedUnixMs = 0
			d.Deadline.Reason = "missing_reference"
			d.Deadline.Resolution = "needs_input"
		},
		func(d *pb.TaskDraftItem) { d.DueAtUnixMs++; d.Deadline.Resolution = "selected" },
		func(d *pb.TaskDraftItem) { d.DueAtUnixMs = 0; d.Deadline.Resolution = "unset" },
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs++
			d.Deadline.Resolution = "selected"
			d.Deadline.ParsedUnixMs = 0
			d.Deadline.Reason = "ambiguous_local_time"
		},
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "none", InstructionReferenceUnixMs: 1791010800123}
		},
		func(d *pb.TaskDraftItem) {
			d.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "selected"}
		},
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "unset"}
		},
	} {
		draft := gatewayParsedDeadline()
		mutate(draft)
		result := deadlineResponse(draft.DueAtUnixMs, 1)
		result.Draft = draft
		w := httptest.NewRecorder()
		writeTaskDraftResult(w, 1, result)
		if w.Code != 200 {
			t.Fatalf("valid metadata %v due=%d: %d %s", draft.Deadline, draft.DueAtUnixMs, w.Code, w.Body.String())
		}
	}
}

func TestDraftDeadlineMetadataRejectsPartialOrContradictoryResults(t *testing.T) {
	for _, mutate := range []func(*pb.TaskDraftItem){
		func(d *pb.TaskDraftItem) { d.Deadline = &pb.TaskDraftDeadline{} },
		func(d *pb.TaskDraftItem) { d.Deadline.Timezone = "" }, func(d *pb.TaskDraftItem) { d.Deadline.Timezone = "UTC" },
		func(d *pb.TaskDraftItem) { d.Deadline.Text = "" }, func(d *pb.TaskDraftItem) { d.Deadline.Text = " 明天" }, func(d *pb.TaskDraftItem) { d.Deadline.Text = strings.Repeat("字", 201) }, func(d *pb.TaskDraftItem) { d.Deadline.Text = string([]byte{0xff}) },
		func(d *pb.TaskDraftItem) { d.Deadline.Source = "unknown" }, func(d *pb.TaskDraftItem) { d.Deadline.SourceMessageId = 0 }, func(d *pb.TaskDraftItem) { d.Deadline.SourceMessageId = -1 },
		func(d *pb.TaskDraftItem) { d.Deadline.ReferenceUnixMs = 0 }, func(d *pb.TaskDraftItem) { d.Deadline.ReferenceUnixMs = maxDraftDeadlineUnixMs + 1 },
		func(d *pb.TaskDraftItem) { d.Deadline.InstructionReferenceUnixMs = -1 }, func(d *pb.TaskDraftItem) { d.Deadline.InstructionReferenceUnixMs = maxDraftDeadlineUnixMs + 1 },
		func(d *pb.TaskDraftItem) { d.Deadline.ParsedUnixMs = -1 }, func(d *pb.TaskDraftItem) { d.Deadline.ParsedUnixMs = maxDraftDeadlineUnixMs + 1 },
		func(d *pb.TaskDraftItem) { d.Deadline.Reason = "unknown" }, func(d *pb.TaskDraftItem) { d.Deadline.Reason = "invalid_date" }, func(d *pb.TaskDraftItem) { d.Deadline.ParsedUnixMs = 0 },
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline.ParsedUnixMs = 0
			d.Deadline.Reason = "missing_reference"
			d.Deadline.Resolution = "needs_input"
		},
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline.Source = "instruction"
			d.Deadline.SourceMessageId = 0
			d.Deadline.ReferenceUnixMs = d.Deadline.InstructionReferenceUnixMs
			d.Deadline.ParsedUnixMs = 0
			d.Deadline.Reason = "missing_reference"
			d.Deadline.Resolution = "needs_input"
		},
		func(d *pb.TaskDraftItem) { d.Deadline.Resolution = "" }, func(d *pb.TaskDraftItem) { d.Deadline.Resolution = "unknown" }, func(d *pb.TaskDraftItem) { d.Deadline.Resolution = "none" },
		func(d *pb.TaskDraftItem) { d.DueAtUnixMs++ }, func(d *pb.TaskDraftItem) { d.Deadline.Resolution = "needs_input" }, func(d *pb.TaskDraftItem) { d.Deadline.Resolution = "unset" },
		func(d *pb.TaskDraftItem) { d.DueAtUnixMs = 0; d.Deadline.Resolution = "selected" },
		func(d *pb.TaskDraftItem) { d.Deadline.Source = "instruction"; d.Deadline.SourceMessageId = 0 },
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "none", Text: "明天"}
		},
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "none", SourceMessageId: 1}
		},
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "none", ReferenceUnixMs: 1}
		},
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "none", ParsedUnixMs: 1}
		},
		func(d *pb.TaskDraftItem) {
			d.DueAtUnixMs = 0
			d.Deadline = &pb.TaskDraftDeadline{Source: "none", Timezone: "Asia/Shanghai", Resolution: "none", Reason: "missing_reference"}
		},
	} {
		draft := gatewayParsedDeadline()
		mutate(draft)
		result := deadlineResponse(draft.DueAtUnixMs, 1)
		result.Draft = draft
		w := httptest.NewRecorder()
		writeTaskDraftResult(w, 1, result)
		if w.Code != 502 {
			t.Fatalf("invalid metadata %v due=%d: %d %s", draft.Deadline, draft.DueAtUnixMs, w.Code, w.Body.String())
		}
	}
}
