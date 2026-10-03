package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskCallingModel struct {
	tools    []*schema.ToolInfo
	generate func([]*schema.Message) (*schema.Message, error)
}

func (m *taskCallingModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &taskCallingModel{tools: tools, generate: m.generate}, nil
}

func (m *taskCallingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if len(m.tools) != 1 || m.tools[0].Name != "list_team_tasks" {
		return nil, errors.New("task tool was not bound to model")
	}
	return m.generate(input)
}

func (*taskCallingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream is not used by synchronous Ask")
}

func listTasksCall() *schema.Message {
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "list_team_tasks", Arguments: `{}`}}})
}

func TestAskCallsTaskToolWithBoundScopeAndUsesResult(t *testing.T) {
	modelCalls, taskCalls := 0, 0
	fakeModel := &taskCallingModel{}
	fakeModel.generate = func(input []*schema.Message) (*schema.Message, error) {
		modelCalls++
		if len(fakeModel.tools) != 0 {
			t.Fatal("base model unexpectedly mutated")
		}
		if len(input) == 2 {
			return listTasksCall(), nil
		}
		if len(input) != 4 || input[2].Role != schema.Assistant || input[3].Role != schema.Tool || input[3].ToolCallID != "call-1" {
			t.Fatalf("wrong tool conversation: %+v", input)
		}
		var result teamTaskToolResult
		if err := json.Unmarshal([]byte(input[3].Content), &result); err != nil || len(result.Tasks) != 1 || result.Tasks[0].TaskID != "9007199254740993" {
			t.Fatalf("tool output = %+v, %v", result, err)
		}
		return schema.AssistantMessage("任务 9007199254740993 需要复核。", nil), nil
	}
	generator, err := NewEinoGroupReplyGenerator(context.Background(), fakeModel)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewContextReader(groupHistoryFunc(func(ctx context.Context, req *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
		if req.GetTeamId() != 200 || req.GetGroupId() != 300 {
			t.Fatalf("IM scope: %v", req)
		}
		return &impb.ListTeamGroupMessagesResponse{Messages: []*impb.TeamGroupMessage{{Id: 3, ContentType: 1, Content: "复核任务"}}}, nil
	}), taskListFunc(func(ctx context.Context, req *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
		taskCalls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || req.GetLimit() != 20 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
			t.Fatalf("wrong task scope/identity: %v, %v", req, md)
		}
		return &taskpb.ListTeamTasksResponse{Tasks: []*taskpb.TaskItem{{TaskId: 9007199254740993, Title: "复核"}}}, nil
	}))
	client := testAskClient(t, NewGroupAnswerer(reader, generator))
	ctx, cancel := askContext()
	defer cancel()
	resp, err := client.Ask(ctx, &pb.AskRequest{TeamId: 200, GroupId: 300, Question: "有哪些任务？"})
	if err != nil || resp.GetAnswer() != "任务 9007199254740993 需要复核。" || modelCalls != 2 || taskCalls != 1 {
		t.Fatalf("Ask = %v, %v; model/task calls = %d/%d", resp, err, modelCalls, taskCalls)
	}
}

func TestAskDoesNotReadTasksWhenModelAnswersWithoutTool(t *testing.T) {
	fakeModel := &taskCallingModel{generate: func(_ []*schema.Message) (*schema.Message, error) {
		return schema.AssistantMessage("群里讨论了缓存。", nil), nil
	}}
	generator, err := NewEinoGroupReplyGenerator(context.Background(), fakeModel)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewContextReader(groupHistoryFunc(func(context.Context, *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
		return &impb.ListTeamGroupMessagesResponse{}, nil
	}), taskListFunc(func(context.Context, *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
		t.Fatal("task RPC called for question answered without tool")
		return nil, nil
	}))
	client := testAskClient(t, NewGroupAnswerer(reader, generator))
	ctx, cancel := askContext()
	defer cancel()
	resp, err := client.Ask(ctx, &pb.AskRequest{TeamId: 200, GroupId: 300, Question: "总结群聊"})
	if err != nil || resp.GetAnswer() != "群里讨论了缓存。" {
		t.Fatalf("Ask = %v, %v", resp, err)
	}
}

func TestAskStopsWhenTaskServiceFails(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			modelCalls := 0
			fakeModel := &taskCallingModel{generate: func(_ []*schema.Message) (*schema.Message, error) {
				modelCalls++
				if modelCalls > 1 {
					t.Fatal("model called after task failure")
				}
				return listTasksCall(), nil
			}}
			generator, err := NewEinoGroupReplyGenerator(context.Background(), fakeModel)
			if err != nil {
				t.Fatal(err)
			}
			reader := NewContextReader(groupHistoryFunc(func(context.Context, *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
				return &impb.ListTeamGroupMessagesResponse{}, nil
			}), taskListFunc(func(context.Context, *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
				return nil, status.Error(code, "task read failed")
			}))
			client := testAskClient(t, NewGroupAnswerer(reader, generator))
			ctx, cancel := askContext()
			defer cancel()
			resp, err := client.Ask(ctx, &pb.AskRequest{TeamId: 200, GroupId: 300, Question: "有哪些任务？"})
			if resp != nil || status.Code(err) != code || modelCalls != 1 {
				t.Fatalf("Ask = %v, %v; calls = %d", resp, err, modelCalls)
			}
		})
	}
}

func TestTaskToolCallRejectsUnsupportedOrRepeatedRequests(t *testing.T) {
	for _, name := range []string{"other_tool", "repeat"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			fakeModel := &taskCallingModel{generate: func(_ []*schema.Message) (*schema.Message, error) {
				calls++
				if name == "other_tool" {
					return schema.AssistantMessage("", []schema.ToolCall{{ID: "call-1", Function: schema.FunctionCall{Name: "other_tool", Arguments: `{}`}}}), nil
				}
				return listTasksCall(), nil
			}}
			generator, err := NewEinoGroupReplyGenerator(context.Background(), fakeModel)
			if err != nil {
				t.Fatal(err)
			}
			reader := NewContextReader(nil, taskListFunc(func(context.Context, *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
				return &taskpb.ListTeamTasksResponse{}, nil
			}))
			tool, err := NewListTeamTasksTool(reader, "user-token", 200)
			if err != nil {
				t.Fatal(err)
			}
			answer, err := generator.Generate(context.Background(), "tasks", nil, tool)
			if answer != "" || status.Code(err) != codes.Internal || (name == "other_tool" && calls != 1) || (name == "repeat" && calls != 2) {
				t.Fatalf("result = %q, %v; calls = %d", answer, err, calls)
			}
		})
	}
}

func TestTaskToolSchemaDoesNotExposeTokenOrTeam(t *testing.T) {
	model := &taskCallingModel{}
	model.generate = func(input []*schema.Message) (*schema.Message, error) { return schema.AssistantMessage("ok", nil), nil }
	generator, err := NewEinoGroupReplyGenerator(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewContextReader(nil, taskListFunc(func(context.Context, *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
		return nil, nil
	}))
	tool, err := NewListTeamTasksTool(reader, "user-token", 200)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect the exact schema passed to WithTools through a separate model below.
	inspect := &inspectingTaskModel{}
	generator.model = inspect
	_, err = generator.Generate(context.Background(), "tasks", nil, tool)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(inspect.tools)
	if err != nil || strings.Contains(string(encoded), "user-token") || strings.Contains(string(encoded), "team_id") {
		t.Fatalf("leaked tool schema: %s, %v", encoded, err)
	}
}

type inspectingTaskModel struct{ tools []*schema.ToolInfo }

func (m *inspectingTaskModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	m.tools = tools
	return m, nil
}
func (*inspectingTaskModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}
func (*inspectingTaskModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("unused")
}
