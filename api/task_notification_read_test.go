package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskNotificationReadFunc func(context.Context, *pb.MarkTaskNotificationReadRequest) (*pb.MarkTaskNotificationReadResponse, error)

func (f taskNotificationReadFunc) MarkTaskNotificationRead(ctx context.Context, req *pb.MarkTaskNotificationReadRequest, _ ...grpc.CallOption) (*pb.MarkTaskNotificationReadResponse, error) {
	return f(ctx, req)
}

func taskNotificationReadHTTPRequest(teamID, notificationID, query string, body io.Reader, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/teams/"+teamID+"/task-notifications/"+notificationID+"/read"+query, body)
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID, "notification_id": notificationID})
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func TestMarkTaskNotificationReadForwardsExactIDsAndToken(t *testing.T) {
	calls := 0
	client := taskNotificationReadFunc(func(ctx context.Context, req *pb.MarkTaskNotificationReadRequest) (*pb.MarkTaskNotificationReadResponse, error) {
		calls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetNotificationId() != 9007199254740997 ||
			len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer original-token" || len(md.Get("idempotency-key")) != 0 {
			t.Fatalf("wrong read RPC request: %v, metadata=%v", req, md)
		}
		return &pb.MarkTaskNotificationReadResponse{NotificationId: req.NotificationId, ReadAtUnixMs: 1791097201123}, nil
	})
	for range 2 {
		w := httptest.NewRecorder()
		markTaskNotificationReadHandler(client)(w, taskNotificationReadHTTPRequest("9007199254740993", "9007199254740997", "", nil, "Bearer original-token"))
		var result struct {
			Code int `json:"code"`
			Data struct {
				NotificationID string `json:"notification_id"`
				ReadAtUnixMs   string `json:"read_at_unix_ms"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || result.Code != errcode.Success || result.Data.NotificationID != "9007199254740997" || result.Data.ReadAtUnixMs != "1791097201123" {
			t.Fatalf("read response: %d %s", w.Code, w.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("repeat was not forwarded: %d", calls)
	}
}

func TestMarkTaskNotificationReadRejectsInvalidHTTPBeforeRPC(t *testing.T) {
	client := taskNotificationReadFunc(func(context.Context, *pb.MarkTaskNotificationReadRequest) (*pb.MarkTaskNotificationReadResponse, error) {
		t.Fatal("invalid HTTP request reached Task RPC")
		return nil, nil
	})
	for _, tc := range []struct {
		name, teamID, notificationID, query string
		body                                io.Reader
		headers                             []string
		want                                int
	}{
		{"missing token", "200", "500", "", nil, nil, 401},
		{"duplicate token", "200", "500", "", nil, []string{"Bearer a", "Bearer b"}, 401},
		{"wrong scheme", "200", "500", "", nil, []string{"Basic token"}, 401},
		{"empty token", "200", "500", "", nil, []string{"Bearer"}, 401},
		{"zero team", "0", "500", "", nil, []string{"Bearer token"}, 400},
		{"signed team", "+200", "500", "", nil, []string{"Bearer token"}, 400},
		{"overflow team", "9223372036854775808", "500", "", nil, []string{"Bearer token"}, 400},
		{"zero notification", "200", "0", "", nil, []string{"Bearer token"}, 400},
		{"negative notification", "200", "-1", "", nil, []string{"Bearer token"}, 400},
		{"overflow notification", "200", "9223372036854775808", "", nil, []string{"Bearer token"}, 400},
		{"query", "200", "500", "?recipient_id=1", nil, []string{"Bearer token"}, 400},
		{"empty query", "200", "500", "?", nil, []string{"Bearer token"}, 400},
		{"body", "200", "500", "", strings.NewReader("{}"), []string{"Bearer token"}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := taskNotificationReadHTTPRequest(tc.teamID, tc.notificationID, tc.query, tc.body, tc.headers...)
			if tc.name == "body" {
				r.ContentLength = 0 // The handler must inspect bytes, not just ContentLength.
			}
			w := httptest.NewRecorder()
			markTaskNotificationReadHandler(client)(w, r)
			if w.Code != tc.want || strings.Contains(w.Body.String(), "token") || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("invalid read request: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMarkTaskNotificationReadMapsRPCFailuresSafely(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
		code int
	}{
		{"invalid", status.Error(codes.InvalidArgument, "private database detail"), 400, errcode.ErrBadRequest},
		{"unauthenticated", status.Error(codes.Unauthenticated, "private database detail"), 401, errcode.ErrUnAuth},
		{"permission", status.Error(codes.PermissionDenied, "private database detail"), 403, errcode.ErrForbidden},
		{"other recipient", status.Error(codes.NotFound, "private database detail"), 404, errcode.ErrNotFound},
		{"unavailable", status.Error(codes.Unavailable, "private database detail"), 503, errcode.ErrInternal},
		{"timeout", status.Error(codes.DeadlineExceeded, "private database detail"), 504, errcode.ErrInternal},
		{"internal", status.Error(codes.Internal, "private database detail"), 502, errcode.ErrInternal},
		{"unknown", errors.New("private database detail"), 502, errcode.ErrInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := taskNotificationReadFunc(func(context.Context, *pb.MarkTaskNotificationReadRequest) (*pb.MarkTaskNotificationReadResponse, error) {
				return nil, tc.err
			})
			w := httptest.NewRecorder()
			markTaskNotificationReadHandler(client)(w, taskNotificationReadHTTPRequest("200", "500", "", nil, "Bearer private-token"))
			var response taskNotificationReadResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.want || response.Code != tc.code || response.Data != nil || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "database") {
				t.Fatalf("unsafe read failure: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMarkTaskNotificationReadRejectsInvalidRPCResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result *pb.MarkTaskNotificationReadResponse
	}{
		{"nil", nil},
		{"wrong notification", &pb.MarkTaskNotificationReadResponse{NotificationId: 501, ReadAtUnixMs: 1791097201123}},
		{"zero time", &pb.MarkTaskNotificationReadResponse{NotificationId: 500}},
		{"negative time", &pb.MarkTaskNotificationReadResponse{NotificationId: 500, ReadAtUnixMs: -1}},
		{"time overflow", &pb.MarkTaskNotificationReadResponse{NotificationId: 500, ReadAtUnixMs: 253402300800000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := taskNotificationReadFunc(func(context.Context, *pb.MarkTaskNotificationReadRequest) (*pb.MarkTaskNotificationReadResponse, error) {
				return tc.result, nil
			})
			w := httptest.NewRecorder()
			markTaskNotificationReadHandler(client)(w, taskNotificationReadHTTPRequest("200", "500", "", nil, "Bearer private-token"))
			if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), `"data"`) || strings.Contains(w.Body.String(), "private-token") {
				t.Fatalf("invalid read result accepted: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

type taskNotificationReadTCPServer struct {
	pb.UnimplementedTaskServer
	calls chan taskNotificationReadTCPCall
}

type taskNotificationReadTCPCall struct {
	teamID         int64
	notificationID int64
	token          []string
}

func (s *taskNotificationReadTCPServer) MarkTaskNotificationRead(ctx context.Context, req *pb.MarkTaskNotificationReadRequest) (*pb.MarkTaskNotificationReadResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.calls <- taskNotificationReadTCPCall{teamID: req.TeamId, notificationID: req.NotificationId, token: md.Get("authorization")}
	return &pb.MarkTaskNotificationReadResponse{NotificationId: req.NotificationId, ReadAtUnixMs: 1791097201123}, nil
}

func TestMarkTaskNotificationReadHTTPOverTCPGRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	task := &taskNotificationReadTCPServer{calls: make(chan taskNotificationReadTCPCall, 1)}
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
	mux.HandleFunc("PUT /api/v1/teams/{team_id}/task-notifications/{notification_id}/read", func(w http.ResponseWriter, r *http.Request) {
		markTaskNotificationReadHandler(pb.NewTaskClient(conn))(w, pathvar.WithVars(r, map[string]string{
			"team_id": r.PathValue("team_id"), "notification_id": r.PathValue("notification_id"),
		}))
	})
	httpServer := httptest.NewServer(mux)
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, httpServer.URL+"/api/v1/teams/9007199254740993/task-notifications/9007199254740997/read", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer original-token")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Data struct {
			NotificationID string `json:"notification_id"`
			ReadAtUnixMs   string `json:"read_at_unix_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || result.Data.NotificationID != "9007199254740997" || result.Data.ReadAtUnixMs != "1791097201123" {
		t.Fatalf("TCP round trip: %d %s", resp.StatusCode, body)
	}
	select {
	case call := <-task.calls:
		if call.teamID != 9007199254740993 || call.notificationID != 9007199254740997 || len(call.token) != 1 || call.token[0] != "Bearer original-token" {
			t.Fatalf("wrong Task gRPC call: %+v", call)
		}
	default:
		t.Fatal("HTTP request did not reach Task over TCP")
	}
}
