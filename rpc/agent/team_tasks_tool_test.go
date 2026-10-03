package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestListTeamTasksToolBindsTrustedScope(t *testing.T) {
	calls := 0
	reader := NewContextReader(nil, taskListFunc(func(ctx context.Context, req *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
		calls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || req.GetLimit() != 20 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer user-token" {
			t.Errorf("wrong task scope or identity: %v, %v", req, md)
		}
		return &taskpb.ListTeamTasksResponse{Tasks: []*taskpb.TaskItem{{TaskId: 9007199254740993, Title: "Review", Status: 1, AssigneeId: 42}}}, nil
	}))
	tool, err := NewListTeamTasksTool(reader, "user-token", 200)
	if err != nil {
		t.Fatal(err)
	}
	info, err := tool.Info(context.Background())
	if err != nil || info.Name != "list_team_tasks" {
		t.Fatalf("tool info = %v, %v", info, err)
	}
	encodedInfo, err := json.Marshal(info)
	if err != nil || strings.Contains(string(encodedInfo), "user-token") || strings.Contains(string(encodedInfo), "team_id") {
		t.Fatalf("tool schema leaks trusted scope: %s, %v", encodedInfo, err)
	}
	output, err := tool.InvokableRun(context.Background(), `{"team_id":999}`)
	if err != nil || calls != 1 {
		t.Fatalf("tool result = %q, %v; calls = %d", output, err, calls)
	}
	var result teamTaskToolResult
	if err := json.Unmarshal([]byte(output), &result); err != nil || len(result.Tasks) != 1 || result.Tasks[0].TaskID != "9007199254740993" || result.Tasks[0].AssigneeID != "42" {
		t.Fatalf("decoded tool result = %+v, %v", result, err)
	}
}

func TestListTeamTasksToolDoesNotReturnDataAfterPermissionFailure(t *testing.T) {
	reader := NewContextReader(nil, taskListFunc(func(context.Context, *taskpb.ListTeamTasksRequest) (*taskpb.ListTeamTasksResponse, error) {
		return nil, status.Error(codes.PermissionDenied, "team membership required")
	}))
	tool, err := NewListTeamTasksTool(reader, "user-token", 200)
	if err != nil {
		t.Fatal(err)
	}
	output, err := tool.InvokableRun(context.Background(), `{}`)
	if output != "" || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unauthorized tool result = %q, %v", output, err)
	}
}
