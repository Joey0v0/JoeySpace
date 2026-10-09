package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type myNotificationFake func(context.Context, *taskpb.ListMyTaskNotificationsRequest) (*taskpb.ListMyTaskNotificationsResponse, error)

func (f myNotificationFake) ListMyTaskNotifications(ctx context.Context, req *taskpb.ListMyTaskNotificationsRequest, _ ...grpc.CallOption) (*taskpb.ListMyTaskNotificationsResponse, error) {
	return f(ctx, req)
}

func TestMyTaskNotificationQueryIsStrict(t *testing.T) {
	for _, raw := range []string{"extra=1", "limit=0", "limit=51", "limit=01", "limit=%2B1", "limit=1&limit=2", "team_id=-1", "team_id=01", "cursor=", "cursor=bad%2Bcursor"} {
		if _, err := parseMyTaskNotificationsQuery(raw); status.Code(err) != codes.InvalidArgument {
			t.Errorf("query %q error=%v", raw, err)
		}
	}
	req, err := parseMyTaskNotificationsQuery("team_id=9007199254740993&cursor=opaque_1&limit=50")
	if err != nil || req.TeamId != 9007199254740993 || req.Cursor != "opaque_1" || req.Limit != 50 {
		t.Fatalf("request=%v err=%v", req, err)
	}
}

func TestMyTaskNotificationsRejectsInvalidRPCWithoutNames(t *testing.T) {
	invalid := []*taskpb.ListMyTaskNotificationsResponse{
		nil,
		{UnreadCount: -1},
		{NextCursor: "bad+cursor"},
		{Notifications: []*taskpb.TaskNotificationItem{{NotificationId: 1, TeamId: 2, TaskId: 3, TaskTitle: "x", ActorId: 4, FromStatus: 1, ToStatus: 1, CreatedAtUnixMs: 5}}},
		{Notifications: []*taskpb.TaskNotificationItem{{NotificationId: 1, TeamId: 2, TaskId: 3, TaskTitle: "x", ActorId: 4, FromStatus: 0, ToStatus: 1, CurrentStatus: 1, CreatedAtUnixMs: 5}, {NotificationId: 1, TeamId: 2, TaskId: 4, TaskTitle: "y", ActorId: 4, FromStatus: 0, ToStatus: 1, CurrentStatus: 1, CreatedAtUnixMs: 6}}},
	}
	for index, response := range invalid {
		namesCalled := false
		client := myNotificationFake(func(context.Context, *taskpb.ListMyTaskNotificationsRequest) (*taskpb.ListMyTaskNotificationsResponse, error) {
			return response, nil
		})
		names := taskNamesFake{teams: func(context.Context, *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
			namesCalled = true
			return nil, nil
		}}
		w := httptest.NewRecorder()
		listMyTaskNotificationsHandler(client, names).ServeHTTP(w, taskReadRequest(""))
		if w.Code != http.StatusBadGateway || namesCalled || strings.Contains(w.Body.String(), `"data"`) {
			t.Errorf("case %d status=%d body=%s names=%v", index, w.Code, w.Body.String(), namesCalled)
		}
	}
}

func TestMyTaskNotificationNamesMayBeMissingButFailuresAbort(t *testing.T) {
	response := &taskpb.ListMyTaskNotificationsResponse{Notifications: []*taskpb.TaskNotificationItem{{NotificationId: 1, TeamId: 2, TaskId: 3, TaskTitle: "x", ActorId: 4, FromStatus: 0, ToStatus: 1, CurrentStatus: 1, CreatedAtUnixMs: 5}}}
	client := myNotificationFake(func(context.Context, *taskpb.ListMyTaskNotificationsRequest) (*taskpb.ListMyTaskNotificationsResponse, error) {
		return response, nil
	})
	w := httptest.NewRecorder()
	listMyTaskNotificationsHandler(client, taskNamesFake{}).ServeHTTP(w, taskReadRequest(""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"team_name":""`) || !strings.Contains(w.Body.String(), `"actor_name":""`) {
		t.Fatalf("missing names response=%d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	listMyTaskNotificationsHandler(client, taskNamesFake{teams: func(context.Context, *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
		return nil, status.Error(codes.Unavailable, "names")
	}}).ServeHTTP(w, taskReadRequest(""))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), `"data"`) {
		t.Fatalf("name failure=%d %s", w.Code, w.Body.String())
	}
}

func TestMyTaskNotificationErrorMappingAndRoute(t *testing.T) {
	for code, want := range map[codes.Code]int{codes.InvalidArgument: 400, codes.Unauthenticated: 401, codes.PermissionDenied: 403, codes.NotFound: 404, codes.Aborted: 409, codes.Unavailable: 503, codes.DeadlineExceeded: 504, codes.Internal: 502} {
		client := myNotificationFake(func(context.Context, *taskpb.ListMyTaskNotificationsRequest) (*taskpb.ListMyTaskNotificationsResponse, error) {
			return nil, status.Error(code, "private")
		})
		w := httptest.NewRecorder()
		listMyTaskNotificationsHandler(client, taskNamesFake{}).ServeHTTP(w, taskReadRequest(""))
		if w.Code != want || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), `"data"`) {
			t.Errorf("code=%v status=%d body=%s", code, w.Code, w.Body.String())
		}
	}
	data, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), `Path:    "/api/v1/task-notifications"`) != 1 || !strings.Contains(string(data), "listMyTaskNotificationsHandler") {
		t.Fatal("exact task notification route missing")
	}
}

func TestMyTaskNotificationEnrichmentRejectsUnexpectedRows(t *testing.T) {
	row := []*taskpb.TaskNotificationItem{{NotificationId: 1, TeamId: 2, TaskId: 3, TaskTitle: "x", ActorId: 4, FromStatus: 0, ToStatus: 1, CurrentStatus: 1, CreatedAtUnixMs: 5}}
	_, err := enrichMyTaskNotifications(context.Background(), taskNamesFake{teams: func(context.Context, *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
		return &userpb.BatchGetMyTeamNamesResponse{Teams: []*userpb.MyTeamName{{TeamId: 99}}}, nil
	}}, row)
	if err == nil {
		t.Fatal("unexpected team name row accepted")
	}
	_, err = enrichMyTaskNotifications(context.Background(), taskNamesFake{teams: func(context.Context, *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
		return nil, errors.New("down")
	}}, row)
	if err == nil {
		t.Fatal("team name failure hidden")
	}
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
