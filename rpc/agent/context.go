package agent

import (
	"context"
	"strings"
	"time"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	contextReadTimeout = 5 * time.Second
	contextPageSize    = 20
)

type groupHistoryClient interface {
	ListTeamGroupMessages(context.Context, *impb.ListTeamGroupMessagesRequest, ...grpc.CallOption) (*impb.ListTeamGroupMessagesResponse, error)
}

type taskListClient interface {
	ListTeamTasks(context.Context, *taskpb.ListTeamTasksRequest, ...grpc.CallOption) (*taskpb.ListTeamTasksResponse, error)
}

type ContextReader struct {
	im    groupHistoryClient
	tasks taskListClient
}

func NewContextReader(im groupHistoryClient, tasks taskListClient) *ContextReader {
	return &ContextReader{im: im, tasks: tasks}
}

// GroupMessages reads at most 20 messages from the current authorized team group.
func (r *ContextReader) GroupMessages(ctx context.Context, token string, teamID, groupID int64) ([]*impb.TeamGroupMessage, error) {
	if r == nil || r.im == nil {
		return nil, status.Error(codes.Unavailable, "IM context is not enabled")
	}
	if teamID <= 0 || groupID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team or group")
	}
	readCtx, cancel, err := authorizedReadContext(ctx, token)
	if err != nil {
		return nil, err
	}
	defer cancel()
	messages, err := r.im.ListTeamGroupMessages(readCtx, &impb.ListTeamGroupMessagesRequest{
		TeamId: teamID, GroupId: groupID, Limit: contextPageSize,
	})
	if err != nil {
		return nil, err
	}
	return messages.GetMessages(), nil
}

// TeamTasks reads at most 20 tasks from the current authorized team.
func (r *ContextReader) TeamTasks(ctx context.Context, token string, teamID int64) ([]*taskpb.TaskItem, error) {
	if r == nil || r.tasks == nil {
		return nil, status.Error(codes.Unavailable, "task context is not enabled")
	}
	if teamID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid team")
	}
	readCtx, cancel, err := authorizedReadContext(ctx, token)
	if err != nil {
		return nil, err
	}
	defer cancel()
	tasks, err := r.tasks.ListTeamTasks(readCtx, &taskpb.ListTeamTasksRequest{
		TeamId: teamID, Limit: contextPageSize,
	})
	if err != nil {
		return nil, err
	}
	return tasks.GetTasks(), nil
}

func authorizedReadContext(ctx context.Context, token string) (context.Context, context.CancelFunc, error) {
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return nil, nil, status.Error(codes.Unauthenticated, "login required")
	}
	readCtx, cancel := context.WithTimeout(ctx, contextReadTimeout)
	return metadata.NewOutgoingContext(readCtx, metadata.Pairs("authorization", "Bearer "+token)), cancel, nil
}
