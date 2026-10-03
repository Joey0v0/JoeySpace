package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/yjydist/go-im/rpc/agent/pb"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type groupReplyFunc func(context.Context, string, []*impb.TeamGroupMessage, tool.InvokableTool) (string, error)

func (f groupReplyFunc) Generate(ctx context.Context, question string, messages []*impb.TeamGroupMessage, taskTool tool.InvokableTool) (string, error) {
	return f(ctx, question, messages, taskTool)
}

func TestAskReadsAuthorizedGroupBeforeGenerating(t *testing.T) {
	readCalls, generateCalls := 0, 0
	reader := NewContextReader(groupHistoryFunc(func(ctx context.Context, req *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
		readCalls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || req.GetGroupId() != 300 || req.GetLimit() != 20 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
			t.Errorf("wrong IM request: %v, %v", req, md)
		}
		return &impb.ListTeamGroupMessagesResponse{Messages: []*impb.TeamGroupMessage{{Id: 400, Content: "Fix the cache"}}}, nil
	}), nil)
	answerer := NewGroupAnswerer(reader, groupReplyFunc(func(_ context.Context, question string, messages []*impb.TeamGroupMessage, taskTool tool.InvokableTool) (string, error) {
		generateCalls++
		if readCalls != 1 || question != "Summarize" || len(messages) != 1 || messages[0].GetContent() != "Fix the cache" || taskTool != nil {
			t.Errorf("wrong generator input: question=%q, messages=%v, reads=%d", question, messages, readCalls)
		}
		return "The team will fix the cache.", nil
	}))
	client := testAskClient(t, answerer)
	ctx, cancel := askContext()
	defer cancel()
	resp, err := client.Ask(ctx, &pb.AskRequest{TeamId: 200, GroupId: 300, Question: "Summarize"})
	if err != nil || resp.GetAnswer() != "The team will fix the cache." || readCalls != 1 || generateCalls != 1 {
		t.Fatalf("Ask = %v, %v; calls = %d/%d", resp, err, readCalls, generateCalls)
	}
}

func TestAskDoesNotGenerateWhenIMRejectsAccess(t *testing.T) {
	for _, code := range []codes.Code{codes.Unauthenticated, codes.PermissionDenied, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			reader := NewContextReader(groupHistoryFunc(func(context.Context, *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
				return nil, status.Error(code, "IM refused group history")
			}), nil)
			answerer := NewGroupAnswerer(reader, groupReplyFunc(func(context.Context, string, []*impb.TeamGroupMessage, tool.InvokableTool) (string, error) {
				t.Fatal("generator called without authorized group history")
				return "", nil
			}))
			client := testAskClient(t, answerer)
			ctx, cancel := askContext()
			defer cancel()
			resp, err := client.Ask(ctx, &pb.AskRequest{TeamId: 200, GroupId: 300, Question: "Summarize"})
			if resp != nil || status.Code(err) != code {
				t.Fatalf("Ask = %v, %v", resp, err)
			}
		})
	}
}
