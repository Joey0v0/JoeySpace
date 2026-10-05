package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskNotificationTransportCall struct {
	request *pb.ListTaskNotificationsRequest
	token   []string
}

type taskNotificationTransportServer struct {
	pb.UnimplementedTaskServer
	calls chan taskNotificationTransportCall
}

type taskNotificationTransportHTTPResponse struct {
	Data struct {
		Notifications []struct {
			NotificationID string `json:"notification_id"`
			TaskID         string `json:"task_id"`
			ActorID        string `json:"actor_id"`
			ReadAtUnixMs   int64  `json:"read_at_unix_ms"`
		} `json:"notifications"`
		NextBeforeNotificationID string `json:"next_before_notification_id"`
	} `json:"data"`
}

func (s *taskNotificationTransportServer) ListTaskNotifications(ctx context.Context, req *pb.ListTaskNotificationsRequest) (*pb.ListTaskNotificationsResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.calls <- taskNotificationTransportCall{request: req, token: md.Get("authorization")}
	if len(md.Get("authorization")) == 1 && md.Get("authorization")[0] == "Bearer revoked-token" {
		return nil, status.Error(codes.PermissionDenied, "private database row 9007199254740997")
	}
	switch req.GetBeforeNotificationId() {
	case 9007199254740999:
		return &pb.ListTaskNotificationsResponse{
			Notifications: []*pb.TaskNotificationItem{
				transportNotification(9007199254740997),
				transportNotification(9007199254740995),
			},
			NextBeforeNotificationId: 9007199254740995,
		}, nil
	case 9007199254740995:
		return &pb.ListTaskNotificationsResponse{
			Notifications: []*pb.TaskNotificationItem{transportNotification(9007199254740993)},
		}, nil
	case 0:
		return &pb.ListTaskNotificationsResponse{}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "unexpected cursor")
	}
}

func transportNotification(id int64) *pb.TaskNotificationItem {
	item := &pb.TaskNotificationItem{
		NotificationId:  id,
		TaskId:          9007199254741001,
		ActorId:         9007199254741003,
		FromStatus:      0,
		ToStatus:        1,
		CreatedAtUnixMs: 1791097200123,
	}
	if id == 9007199254740997 {
		item.ReadAtUnixMs = 1791097201123
	}
	return item
}

func TestTaskNotificationsHTTPOverTCPGRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	task := &taskNotificationTransportServer{calls: make(chan taskNotificationTransportCall, 5)}
	rpcServer := grpc.NewServer()
	pb.RegisterTaskServer(rpcServer, task)
	go func() { _ = rpcServer.Serve(listener) }()
	t.Cleanup(rpcServer.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/teams/{team_id}/task-notifications", func(w http.ResponseWriter, r *http.Request) {
		listTaskNotificationsHandler(pb.NewTaskClient(conn))(w, pathvar.WithVars(r, map[string]string{"team_id": r.PathValue("team_id")}))
	})
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	httpClient := &http.Client{Timeout: 3 * time.Second}
	request := func(token, query string) (int, []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/v1/teams/9007199254740993/task-notifications"+query, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body
	}
	assertCall := func(token string, cursor int64, limit int32) {
		t.Helper()
		select {
		case call := <-task.calls:
			if call.request.GetTeamId() != 9007199254740993 || call.request.GetBeforeNotificationId() != cursor || call.request.GetLimit() != limit ||
				len(call.token) != 1 || call.token[0] != "Bearer "+token {
				t.Fatalf("wrong TCP gRPC request: %v, authorization=%v", call.request, call.token)
			}
		default:
			t.Fatal("HTTP request did not reach Task gRPC")
		}
	}
	decode := func(body []byte) taskNotificationTransportHTTPResponse {
		t.Helper()
		var value taskNotificationTransportHTTPResponse
		if err := json.Unmarshal(body, &value); err != nil {
			t.Fatalf("decode HTTP JSON: %v", err)
		}
		return value
	}

	code, body := request("original-token", "?before_notification_id=9007199254740999&limit=2")
	assertCall("original-token", 9007199254740999, 2)
	first := decode(body)
	if code != http.StatusOK || len(first.Data.Notifications) != 2 || first.Data.NextBeforeNotificationID != "9007199254740995" ||
		first.Data.Notifications[0].NotificationID != "9007199254740997" || first.Data.Notifications[0].TaskID != "9007199254741001" || first.Data.Notifications[0].ActorID != "9007199254741003" ||
		first.Data.Notifications[0].ReadAtUnixMs != 1791097201123 ||
		first.Data.Notifications[1].NotificationID != "9007199254740995" || first.Data.Notifications[1].ReadAtUnixMs != 0 {
		t.Fatalf("first page or exact string IDs: %d %s", code, body)
	}

	code, body = request("original-token", "?before_notification_id=9007199254740995&limit=2")
	assertCall("original-token", 9007199254740995, 2)
	final := decode(body)
	if code != http.StatusOK || len(final.Data.Notifications) != 1 || final.Data.Notifications[0].NotificationID != "9007199254740993" || final.Data.NextBeforeNotificationID != "0" {
		t.Fatalf("final page: %d %s", code, body)
	}

	code, body = request("original-token", "")
	assertCall("original-token", 0, 20)
	empty := decode(body)
	if code != http.StatusOK || empty.Data.Notifications == nil || len(empty.Data.Notifications) != 0 || empty.Data.NextBeforeNotificationID != "0" {
		t.Fatalf("empty page: %d %s", code, body)
	}

	code, body = request("revoked-token", "?limit=2")
	assertCall("revoked-token", 0, 2)
	if code != http.StatusForbidden || strings.Contains(string(body), "private database") || strings.Contains(string(body), "9007199254740997") || strings.Contains(string(body), "revoked-token") || strings.Contains(string(body), `"data"`) {
		t.Fatalf("unsafe permission response: %d %s", code, body)
	}

	code, body = request("original-token", "?recipient_id=9007199254740997")
	if code != http.StatusBadRequest || strings.Contains(string(body), `"data"`) {
		t.Fatalf("recipient query accepted: %d %s", code, body)
	}
	select {
	case call := <-task.calls:
		t.Fatalf("recipient query reached Task gRPC: %v", call.request)
	default:
	}
}
