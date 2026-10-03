package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func batchModelItem() map[string]any {
	return map[string]any{"title": "修复缓存", "description": "", "source_message_id": "9007199254740993", "assignee_name": "张三", "deadline_text": "明天 15:30", "deadline_source": "message", "deadline_source_message_id": "9007199254740995"}
}

func batchModelJSON(t *testing.T, items ...any) string {
	t.Helper()
	value, err := json.Marshal(map[string]any{"drafts": items})
	if err != nil {
		t.Fatal(err)
	}
	return string(value)
}

func TestEinoTaskDraftsUsesOneModelCallAndPreciseIndependentIDs(t *testing.T) {
	calls := 0
	second := batchModelItem()
	second["title"], second["assignee_name"] = "整理文档", "李四"
	second["source_message_id"], second["deadline_source_message_id"] = "9007199254740995", "0"
	second["deadline_source"], second["deadline_text"] = "instruction", "明天下午"
	g, err := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(_ context.Context, input []*schema.Message) (*schema.Message, error) {
		calls++
		if len(input) != 2 || input[0].Content != taskDraftsInstructions || !strings.Contains(input[0].Content, `{"drafts":[...]}`) {
			t.Fatalf("batch prompt = %+v", input)
		}
		var data struct {
			Instruction string                   `json:"instruction"`
			Messages    []taskDraftPromptMessage `json:"group_messages"`
		}
		if err := json.Unmarshal([]byte(input[1].Content), &data); err != nil {
			t.Fatal(err)
		}
		if data.Instruction != "李四明天下午整理文档" || len(data.Messages) != 2 || data.Messages[0].MessageID != "9007199254740995" || data.Messages[1].MessageID != "9007199254740993" || data.Messages[1].SenderID != "9007199254740997" {
			t.Fatalf("model context = %+v", data)
		}
		return schema.AssistantMessage(batchModelJSON(t, batchModelItem(), second), nil), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	drafts, err := g.GenerateDrafts(context.Background(), "李四明天下午整理文档", []*impb.TeamGroupMessage{
		{Id: 9007199254740993, FromId: 9007199254740997, ContentType: 1, Content: "张三修复缓存"},
		nil, {Id: 42, ContentType: 2, Content: "private file"},
		{Id: 9007199254740995, ContentType: 1, Content: "明天 15:30"},
	})
	if err != nil || calls != 1 || len(drafts) != 2 {
		t.Fatalf("drafts = %+v, calls=%d, %v", drafts, calls, err)
	}
	if drafts[0].SourceMessageID != 9007199254740993 || drafts[0].Deadline.SourceMessageID != 9007199254740995 || drafts[1].AssigneeName != "李四" || drafts[1].Deadline.Source != "instruction" {
		t.Fatalf("independent evidence = %+v", drafts)
	}
	for _, draft := range drafts {
		if draft.AssigneeID != 0 || draft.DueAtUnixMs != 0 || draft.AssigneeResolution != "" || draft.Deadline.Resolution != "" || draft.Deadline.ReferenceUnixMs != 0 {
			t.Fatalf("model supplied trusted state: %+v", draft)
		}
	}
}

func TestEinoTaskDraftsRejectsWholeInvalidCollection(t *testing.T) {
	valid := batchModelItem()
	cases := map[string]string{
		"zero": `{"drafts":[]}`, "null": `{"drafts":null}`, "missing": `{}`,
		"wrong envelope": `[]`, "wrong type": `{"drafts":{}}`, "unknown envelope": `{"drafts":[],"task_id":"1"}`,
		"trailing": batchModelJSON(t, valid) + `{}`, "null item": batchModelJSON(t, valid, nil),
		"six items": batchModelJSON(t, valid, valid, valid, valid, valid, valid),
	}
	for _, field := range []string{"title", "description", "source_message_id", "assignee_name", "deadline_text", "deadline_source", "deadline_source_message_id"} {
		for _, kind := range []string{"missing", "null", "number"} {
			bad := batchModelItem()
			switch kind {
			case "missing":
				delete(bad, field)
			case "null":
				bad[field] = nil
			case "number":
				bad[field] = 1
			}
			cases[field+" "+kind] = batchModelJSON(t, valid, bad)
		}
	}
	for _, field := range []string{"assignee_id", "due_at_unix_ms", "deadline_resolution", "created_task_id"} {
		bad := batchModelItem()
		bad[field] = "1"
		cases[field] = batchModelJSON(t, valid, bad)
	}
	for _, field := range []string{"source_message_id", "deadline_source_message_id"} {
		for _, id := range []string{"-1", "01", "+1", "1.0", "9223372036854775808"} {
			bad := batchModelItem()
			bad[field] = id
			cases[field+id] = batchModelJSON(t, valid, bad)
		}
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			g, err := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
				return schema.AssistantMessage(output, nil), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			drafts, err := g.GenerateDrafts(context.Background(), "提取待办", nil)
			if drafts != nil || status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("partial result = %+v, %v", drafts, err)
			}
		})
	}
}

func TestEinoTaskDraftsBoundsAndModelErrors(t *testing.T) {
	for _, count := range []int{1, 5} {
		items := make([]any, count)
		for i := range items {
			items[i] = batchModelItem()
		}
		g, err := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) {
			return schema.AssistantMessage(batchModelJSON(t, items...), nil), nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if drafts, err := g.GenerateDrafts(context.Background(), "提取待办", nil); err != nil || len(drafts) != count {
			t.Fatalf("count=%d: %+v, %v", count, drafts, err)
		}
	}
	for _, tc := range []struct {
		name   string
		answer *schema.Message
		err    error
		code   codes.Code
	}{
		{"nil", nil, nil, codes.FailedPrecondition},
		{"role", schema.UserMessage("{}"), nil, codes.FailedPrecondition},
		{"tool call", &schema.Message{Role: schema.Assistant, Content: batchModelJSON(t, batchModelItem()), ToolCalls: []schema.ToolCall{{ID: "write-task"}}}, nil, codes.FailedPrecondition},
		{"oversize", schema.AssistantMessage(strings.Repeat(" ", 5*16384+1), nil), nil, codes.FailedPrecondition},
		{"provider", nil, errors.New("private provider detail"), codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := NewEinoTaskDraftGenerator(context.Background(), chatModelFunc(func(context.Context, []*schema.Message) (*schema.Message, error) { return tc.answer, tc.err }))
			if err != nil {
				t.Fatal(err)
			}
			drafts, err := g.GenerateDrafts(context.Background(), "提取待办", nil)
			if drafts != nil || status.Code(err) != tc.code || strings.Contains(err.Error(), "private provider") {
				t.Fatalf("result = %+v, %v", drafts, err)
			}
		})
	}
	var g *EinoTaskDraftGenerator
	if drafts, err := g.GenerateDrafts(context.Background(), "提取待办", nil); drafts != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("nil generator = %+v, %v", drafts, err)
	}
}
