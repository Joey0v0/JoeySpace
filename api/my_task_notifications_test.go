package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
)

type myNotificationFake func(context.Context, *taskpb.ListMyTaskNotificationsRequest) (*taskpb.ListMyTaskNotificationsResponse, error)

func (f myNotificationFake) ListMyTaskNotifications(ctx context.Context, req *taskpb.ListMyTaskNotificationsRequest, _ ...grpc.CallOption) (*taskpb.ListMyTaskNotificationsResponse, error) {
	return f(ctx, req)
}

func TestMyTaskNotificationsQueryEnrichmentAndStringBoundaries(t *testing.T) {
	client := myNotificationFake(func(ctx context.Context, req *taskpb.ListMyTaskNotificationsRequest) (*taskpb.ListMyTaskNotificationsResponse, error) {
		assertTaskReadBearer(t, ctx)
		if req.GetTeamId() != 0 || req.GetCursor() != "opaque_1" || req.GetLimit() != 20 {
			t.Fatalf("request=%v", req)
		}
		return &taskpb.ListMyTaskNotificationsResponse{UnreadCount: 1, Notifications: []*taskpb.TaskNotificationItem{{NotificationId: 9007199254740993, TeamId: 100, TaskId: 9007199254740995, TaskTitle: "发布", ActorId: 5, FromStatus: 0, ToStatus: 1, CurrentStatus: 1, CreatedAtUnixMs: 1790874000123}}}, nil
	})
	names := taskNamesFake{
		teams: func(context.Context, *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
			return &userpb.BatchGetMyTeamNamesResponse{Teams: []*userpb.MyTeamName{{TeamId: 100, Name: "平台组"}}}, nil
		},
		members: func(context.Context, *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
			return &userpb.BatchGetTeamMemberDisplayNamesResponse{Users: []*userpb.TeamMemberDisplayName{{UserId: 5, DisplayName: "小乔"}}}, nil
		},
	}
	r := taskReadRequest("cursor=opaque_1&limit=20")
	w := httptest.NewRecorder()
	listMyTaskNotificationsHandler(client, names).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	data := body["data"].(map[string]any)
	row := data["notifications"].([]any)[0].(map[string]any)
	if row["notification_id"] != "9007199254740993" || row["created_at_unix_ms"] != "1790874000123" || data["unread_count"] != "1" || row["team_name"] != "平台组" || row["actor_name"] != "小乔" {
		t.Fatalf("body=%s", w.Body.String())
	}
}

func TestReadNotificationTimestampIsString(t *testing.T) {
	body, err := json.Marshal(taskNotificationReadData{NotificationID: 9, ReadAtUnixMs: 1790874000123})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != `{"notification_id":"9","read_at_unix_ms":"1790874000123"}` {
		t.Fatalf("body=%s", body)
	}
}
