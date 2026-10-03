package agent

import (
	"context"
	"testing"

	impb "github.com/yjydist/go-im/rpc/im/pb"
	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type groupHistoryFunc func(context.Context, *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error)

func (f groupHistoryFunc) ListTeamGroupMessages(ctx context.Context, req *impb.ListTeamGroupMessagesRequest, _ ...grpc.CallOption) (*impb.ListTeamGroupMessagesResponse, error) {
	return f(ctx, req)
}

type taskListFunc func(context.Context, *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error)

func (f taskListFunc) ListTeamTasks(ctx context.Context, req *taskpb.ListTeamTasksRequest, _ ...grpc.CallOption) (*taskpb.ListTeamTasksResponse, error) {
	return f(ctx, req)
}

func TestContextReaderCallsOnlyRequestedAuthorizedService(t *testing.T) {
	imCalls, taskCalls := 0, 0
	reader := NewContextReader(
		groupHistoryFunc(func(ctx context.Context, req *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
			imCalls++
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.GetTeamId() != 200 || req.GetGroupId() != 300 || req.GetLimit() != 20 || req.GetBeforeMessageId() != 0 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
				t.Fatalf("wrong IM request or identity: %v, %v", req, md)
			}
			return &impb.ListTeamGroupMessagesResponse{Messages: []*impb.TeamGroupMessage{{Id: 400, Content: "fix cache"}}}, nil
		}),
		taskListFunc(func(ctx context.Context, req *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
			taskCalls++
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.GetTeamId() != 200 || req.GetLimit() != 20 || req.GetAfterTaskId() != 0 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
				t.Fatalf("wrong task request or identity: %v, %v", req, md)
			}
			return &taskpb.ListTeamTasksResponse{Tasks: []*taskpb.TaskItem{{TaskId: 500, Title: "Review"}}}, nil
		}),
	)
	messages, err := reader.GroupMessages(context.Background(), "user-token", 200, 300)
	if err != nil || len(messages) != 1 || messages[0].GetId() != 400 || imCalls != 1 || taskCalls != 0 {
		t.Fatalf("messages = %v, error = %v, calls = %d/%d", messages, err, imCalls, taskCalls)
	}
	tasks, err := reader.TeamTasks(context.Background(), "user-token", 200)
	if err != nil || len(tasks) != 1 || tasks[0].GetTaskId() != 500 || imCalls != 1 || taskCalls != 1 {
		t.Fatalf("tasks = %v, error = %v, calls = %d/%d", tasks, err, imCalls, taskCalls)
	}
}

func TestContextReaderDoesNotExposeDataAfterPermissionFailure(t *testing.T) {
	reader := NewContextReader(
		groupHistoryFunc(func(context.Context, *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "group membership required")
		}),
		taskListFunc(func(context.Context, *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "team membership required")
		}),
	)
	messages, err := reader.GroupMessages(context.Background(), "user-token", 200, 300)
	if messages != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthorized messages = %v, error = %v", messages, err)
	}
	tasks, err := reader.TeamTasks(context.Background(), "user-token", 200)
	if tasks != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthorized tasks = %v, error = %v", tasks, err)
	}
}

func TestContextReaderRejectsMissingIdentityAndScope(t *testing.T) {
	reader := NewContextReader(
		groupHistoryFunc(func(context.Context, *impb.ListTeamGroupMessagesRequest) (*impb.ListTeamGroupMessagesResponse, error) {
			t.Fatal("invalid input reached IM")
			return nil, nil
		}),
		taskListFunc(func(context.Context, *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
			t.Fatal("invalid input reached task service")
			return nil, nil
		}),
	)
	for _, tc := range []struct {
		token           string
		teamID, groupID int64
		want            codes.Code
	}{
		{"", 200, 300, codes.Unauthenticated},
		{"bad token", 200, 300, codes.Unauthenticated},
		{"user-token", 0, 300, codes.InvalidArgument},
		{"user-token", 200, 0, codes.InvalidArgument},
	} {
		messages, err := reader.GroupMessages(context.Background(), tc.token, tc.teamID, tc.groupID)
		if messages != nil || status.Code(err) != tc.want {
			t.Fatalf("messages for %q %d/%d = %v, %v", tc.token, tc.teamID, tc.groupID, messages, err)
		}
	}
	for _, tc := range []struct {
		token  string
		teamID int64
		want   codes.Code
	}{
		{"", 200, codes.Unauthenticated},
		{"bad token", 200, codes.Unauthenticated},
		{"user-token", 0, codes.InvalidArgument},
	} {
		tasks, err := reader.TeamTasks(context.Background(), tc.token, tc.teamID)
		if tasks != nil || status.Code(err) != tc.want {
			t.Fatalf("tasks for %q %d = %v, %v", tc.token, tc.teamID, tasks, err)
		}
	}
}
