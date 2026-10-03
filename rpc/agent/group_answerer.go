package agent

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GroupReplyGenerator receives IM-authorized messages and an optional task
// tool whose user and team scope are fixed by the server.
type GroupReplyGenerator interface {
	Generate(context.Context, string, []*impb.TeamGroupMessage, tool.InvokableTool) (string, error)
}

type GroupAnswerer struct {
	reader    *ContextReader
	generator GroupReplyGenerator
}

func NewGroupAnswerer(reader *ContextReader, generator GroupReplyGenerator) *GroupAnswerer {
	return &GroupAnswerer{reader: reader, generator: generator}
}

func (a *GroupAnswerer) Answer(ctx context.Context, token string, teamID, groupID int64, question string) (string, error) {
	if a == nil || a.reader == nil || a.generator == nil {
		return "", status.Error(codes.Unavailable, "group answerer is not configured")
	}
	messages, err := a.reader.GroupMessages(ctx, token, teamID, groupID)
	if err != nil {
		return "", err
	}
	if ctx.Err() != nil {
		return "", status.FromContextError(ctx.Err()).Err()
	}
	var taskTool tool.InvokableTool
	if a.reader.tasks != nil {
		taskTool, err = NewListTeamTasksTool(a.reader, token, teamID)
		if err != nil {
			return "", err
		}
	}
	return a.generator.Generate(ctx, question, messages, taskTool)
}
