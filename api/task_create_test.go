package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/internal/pkg/errcode"
	"github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskCreatorFunc func(context.Context, *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error)

func (f taskCreatorFunc) CreateTask(ctx context.Context, req *pb.CreateTaskRequest, _ ...grpc.CallOption) (*pb.CreateTaskResponse, error) {
	return f(ctx, req)
}

func taskHTTPRequest(teamID, body string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/teams/"+teamID+"/tasks", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID})
	r.Header.Set("Idempotency-Key", "request-123")
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func TestGatewayConfigLoadsTaskRPC(t *testing.T) {
	for _, tc := range []struct{ file, endpoint string }{
		{"etc/api.yaml", "127.0.0.1:9003"},
		{"../deploy/api-gateway.yaml", "task-rpc:9003"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			var c config
			if err := conf.Load(tc.file, &c); err != nil {
				t.Fatal(err)
			}
			if len(c.TaskRPC.Endpoints) != 1 || c.TaskRPC.Endpoints[0] != tc.endpoint {
				t.Fatalf("Task RPC endpoints = %v, want %s", c.TaskRPC.Endpoints, tc.endpoint)
			}
		})
	}
}

func TestCreateTaskHTTPForwardsSafeStringIDsAndCredentials(t *testing.T) {
	client := taskCreatorFunc(func(ctx context.Context, req *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetAssigneeId() != 9007199254740995 || req.GetSourceGroupId() != 9007199254740999 || req.GetSourceMessageId() != 9007199254741001 || req.GetDueAtUnixMs() != 1790874000000 || req.GetTitle() != "Planning" || req.GetDescription() != "Check tasks" || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" || len(md.Get("idempotency-key")) != 1 || md.Get("idempotency-key")[0] != "request-123" {
			t.Fatalf("wrong RPC request or metadata: %v, %v", req, md)
		}
		return &pb.CreateTaskResponse{TaskId: 9007199254740997}, nil
	})
	w := httptest.NewRecorder()
	createTaskHandler(client)(w, taskHTTPRequest("9007199254740993", `{"title":" Planning ","description":" Check tasks ","assignee_id":"9007199254740995","source_group_id":"9007199254740999","source_message_id":"9007199254741001","due_at_unix_ms":1790874000000}`, "Bearer test-token"))
	var result struct {
		Code int `json:"code"`
		Data struct {
			TaskID string `json:"task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || result.Code != 0 || result.Data.TaskID != "9007199254740997" || strings.Contains(w.Body.String(), "test-token") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestCreateTaskHTTPWithoutDueTimeForwardsZero(t *testing.T) {
	client := taskCreatorFunc(func(_ context.Context, req *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
		if req.GetDueAtUnixMs() != 0 {
			t.Fatalf("due time = %d, want 0", req.GetDueAtUnixMs())
		}
		return &pb.CreateTaskResponse{TaskId: 1}, nil
	})
	w := httptest.NewRecorder()
	createTaskHandler(client)(w, taskHTTPRequest("100", `{"title":"Task"}`, "Bearer token"))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestCreateTaskHTTPRejectsBadInputBeforeRPC(t *testing.T) {
	client := taskCreatorFunc(func(context.Context, *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
		t.Fatal("invalid input reached Task RPC")
		return nil, nil
	})
	for _, tc := range []struct {
		teamID, body string
		headers      []string
		want         int
	}{
		{"100", `{"title":"Task"}`, nil, 401},
		{"100", `{"title":"Task"}`, []string{"Bearer a", "Bearer b"}, 401},
		{"100", `{"title":"Task"}`, []string{"Basic token"}, 401},
		{"0", `{"title":"Task"}`, []string{"Bearer token"}, 400},
		{"bad", `{"title":"Task"}`, []string{"Bearer token"}, 400},
		{"100", `{}`, []string{"Bearer token"}, 400},
		{"100", `{"title":" "}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","assignee_id":9007199254740995}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","assignee_id":"-1"}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","assignee_id":"9223372036854775808"}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","source_group_id":"300"}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","source_message_id":"400"}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","source_group_id":300,"source_message_id":"400"}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","source_group_id":"-1","source_message_id":"400"}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","due_at_unix_ms":-1}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","due_at_unix_ms":253402300800000}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","due_at_unix_ms":"1790874000000"}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","due_at_unix_ms":1790874000000.5}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task","creator_id":"42"}`, []string{"Bearer token"}, 400},
		{"100", `{"title":"Task"}{"title":"Other"}`, []string{"Bearer token"}, 400},
	} {
		w := httptest.NewRecorder()
		createTaskHandler(client)(w, taskHTTPRequest(tc.teamID, tc.body, tc.headers...))
		if w.Code != tc.want {
			t.Fatalf("team=%q body=%q: got %d %s", tc.teamID, tc.body, w.Code, w.Body.String())
		}
	}
	for _, mutate := range []func(http.Header){
		func(h http.Header) { h.Del("Idempotency-Key") },
		func(h http.Header) { h.Set("Idempotency-Key", "") },
		func(h http.Header) { h.Set("Idempotency-Key", strings.Repeat("a", 65)) },
		func(h http.Header) { h.Set("Idempotency-Key", "request-123,other") },
		func(h http.Header) { h.Set("Idempotency-Key", "has space") },
		func(h http.Header) { h.Add("Idempotency-Key", "another-request") },
	} {
		w := httptest.NewRecorder()
		r := taskHTTPRequest("100", `{"title":"Task"}`, "Bearer token")
		mutate(r.Header)
		createTaskHandler(client)(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("keys %v: got %d %s", r.Header.Values("Idempotency-Key"), w.Code, w.Body.String())
		}
	}
}

func TestCreateTaskHTTPMapsRPCErrors(t *testing.T) {
	for _, tc := range []struct {
		code                 codes.Code
		wantStatus, wantCode int
	}{
		{codes.InvalidArgument, 400, errcode.ErrBadRequest},
		{codes.Unauthenticated, 401, errcode.ErrUnAuth},
		{codes.PermissionDenied, 403, errcode.ErrForbidden},
		{codes.NotFound, 404, errcode.ErrNotFound},
		{codes.FailedPrecondition, 409, errcode.ErrUserBanned},
		{codes.AlreadyExists, 409, errcode.ErrTaskRequestConflict},
		{codes.Unavailable, 503, errcode.ErrInternal},
		{codes.DeadlineExceeded, 504, errcode.ErrInternal},
		{codes.Internal, 502, errcode.ErrInternal},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			client := taskCreatorFunc(func(context.Context, *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
				return nil, status.Error(tc.code, "private database detail")
			})
			w := httptest.NewRecorder()
			createTaskHandler(client)(w, taskHTTPRequest("100", `{"title":"Task"}`, "Bearer token"))
			var result createTaskResponse
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.wantStatus || result.Code != tc.wantCode || result.Data != nil || strings.Contains(w.Body.String(), "private database detail") {
				t.Fatalf("RPC %v: got %d %s", tc.code, w.Code, w.Body.String())
			}
		})
	}
}

func TestCreateTaskHTTPIdenticalRetryForwardsOneUnchangedKey(t *testing.T) {
	calls := 0
	key := "Retry_Aa-09."
	client := taskCreatorFunc(func(ctx context.Context, req *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
		calls++
		md, _ := metadata.FromOutgoingContext(ctx)
		if got := md.Get("idempotency-key"); len(got) != 1 || got[0] != key {
			t.Fatalf("request key: %v", got)
		}
		if req.GetTeamId() != 100 || req.GetTitle() != "Task" || req.GetDescription() != "Same body" {
			t.Fatalf("retry body: %v", req)
		}
		return &pb.CreateTaskResponse{TaskId: 123}, nil
	})
	for i := 0; i < 2; i++ {
		r := taskHTTPRequest("100", `{"title":"Task","description":"Same body"}`, "Bearer token")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		createTaskHandler(client)(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("identical retry: %d %s", w.Code, w.Body.String())
		}
	}
	if calls != 2 {
		t.Fatalf("RPC calls = %d", calls)
	}
}
