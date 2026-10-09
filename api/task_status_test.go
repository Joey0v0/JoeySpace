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
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskStatusFunc func(context.Context, *pb.SetTaskStatusRequest) (*pb.SetTaskStatusResponse, error)

func (f taskStatusFunc) SetTaskStatus(ctx context.Context, req *pb.SetTaskStatusRequest, _ ...grpc.CallOption) (*pb.SetTaskStatusResponse, error) {
	return f(ctx, req)
}

func taskStatusHTTPRequest(teamID, taskID, body string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/teams/"+teamID+"/tasks/"+taskID+"/status", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID, "task_id": taskID})
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func TestSetTaskStatusHTTPForwardsLargeIDsAndZeroStatus(t *testing.T) {
	client := taskStatusFunc(func(ctx context.Context, req *pb.SetTaskStatusRequest) (*pb.SetTaskStatusResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetTaskId() != 9007199254740995 || req.GetStatus() != 0 || req.ExpectedStatus == nil || req.GetExpectedStatus() != 0 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer token" {
			t.Fatalf("wrong RPC request or token: %v, %v", req, md)
		}
		return &pb.SetTaskStatusResponse{}, nil
	})
	w := httptest.NewRecorder()
	setTaskStatusHandler(client)(w, taskStatusHTTPRequest("9007199254740993", "9007199254740995", `{"status":0,"expected_status":0}`, "  bearer   token  "))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatalf("status response: %d %s", w.Code, w.Body.String())
	}
}

func TestSetTaskStatusHTTPRejectsInvalidInput(t *testing.T) {
	client := taskStatusFunc(func(context.Context, *pb.SetTaskStatusRequest) (*pb.SetTaskStatusResponse, error) {
		t.Fatal("invalid status request reached Task RPC")
		return nil, nil
	})
	for _, tc := range []struct {
		teamID, taskID, body string
		headers              []string
		want                 int
	}{
		{"200", "500", `{"status":1}`, nil, 401},
		{"200", "500", `{"status":1}`, []string{"Bearer a", "Bearer b"}, 401},
		{"200", "500", `{"status":1}`, []string{"Basic token"}, 401},
		{"0", "500", `{"status":1}`, []string{"Bearer token"}, 400},
		{"200", "bad", `{"status":1}`, []string{"Bearer token"}, 400},
		{"200", "500", `{}`, []string{"Bearer token"}, 400},
		{"200", "500", `{"status":null}`, []string{"Bearer token"}, 400},
		{"200", "500", `{"status":-1}`, []string{"Bearer token"}, 400},
		{"200", "500", `{"status":3}`, []string{"Bearer token"}, 400},
		{"200", "500", `{"status":"1"}`, []string{"Bearer token"}, 400},
		{"200", "500", `{"status":1,"actor_id":"42"}`, []string{"Bearer token"}, 400},
		{"200", "500", `{"status":1}{"status":2}`, []string{"Bearer token"}, 400},
	} {
		w := httptest.NewRecorder()
		setTaskStatusHandler(client)(w, taskStatusHTTPRequest(tc.teamID, tc.taskID, tc.body, tc.headers...))
		if w.Code != tc.want {
			t.Fatalf("team=%q task=%q body=%q: %d %s", tc.teamID, tc.taskID, tc.body, w.Code, w.Body.String())
		}
	}
}

func TestSetTaskStatusHTTPMapsFailuresWithoutLeakingDetails(t *testing.T) {
	for _, tc := range []struct {
		code     codes.Code
		want     int
		wantCode int
	}{
		{codes.InvalidArgument, 400, errcode.ErrBadRequest},
		{codes.Unauthenticated, 401, errcode.ErrUnAuth},
		{codes.PermissionDenied, 403, errcode.ErrForbidden},
		{codes.NotFound, 404, errcode.ErrNotFound},
		{codes.Aborted, 409, errcode.ErrBadRequest},
		{codes.Unavailable, 503, errcode.ErrInternal},
		{codes.DeadlineExceeded, 504, errcode.ErrInternal},
		{codes.Internal, 502, errcode.ErrInternal},
	} {
		client := taskStatusFunc(func(context.Context, *pb.SetTaskStatusRequest) (*pb.SetTaskStatusResponse, error) {
			return nil, status.Error(tc.code, "private database detail")
		})
		w := httptest.NewRecorder()
		setTaskStatusHandler(client)(w, taskStatusHTTPRequest("200", "500", `{"status":2,"expected_status":1}`, "Bearer token"))
		var result taskStatusResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if tc.code == codes.Aborted && (result.Code != errcode.ErrBadRequest || result.Msg != "task status changed; reload and confirm before retrying") {
			t.Fatalf("conflict response: %d %s", w.Code, w.Body.String())
		}
		if w.Code != tc.want || result.Code != tc.wantCode || strings.Contains(w.Body.String(), "private database detail") {
			t.Fatalf("RPC %v: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}

func TestSetTaskStatusHTTPRejectsInvalidStatusBodiesBeforeRPC(t *testing.T) {
	bodies := []string{
		`{"status":1}`, `{"expected_status":0}`, `{}`, `null`, `[]`,
		`{"status":null,"expected_status":0}`, `{"status":1,"expected_status":null}`,
		`{"status":"1","expected_status":0}`, `{"status":1,"expected_status":"0"}`,
		`{"status":1.0,"expected_status":0}`, `{"status":1,"expected_status":0.0}`,
		`{"status":-1,"expected_status":0}`, `{"status":3,"expected_status":0}`,
		`{"status":1,"expected_status":-1}`, `{"status":1,"expected_status":3}`,
		`{"status":1,"expected_status":2147483648}`, `{"status":2147483648,"expected_status":0}`,
		`{"status":1,"status":2,"expected_status":0}`, `{"status":1,"expected_status":0,"expected_status":1}`,
		`{"status":1,"expected_status":0,"actor_id":42}`,
		`{"status":1,"expected_status":0}{"status":2,"expected_status":0}`,
		`{"status":1,"expected_status":0} null`, `{"status":1,"expected_status":0} trailing`,
		`{"status":1,"expected_status":0`, `{"Status":1,"expected_status":0}`,
		`{"status":1,"expected_status":0}` + strings.Repeat(" ", 4096),
	}
	for _, body := range bodies {
		t.Run(body[:min(len(body), 80)], func(t *testing.T) {
			client := taskStatusFunc(func(context.Context, *pb.SetTaskStatusRequest) (*pb.SetTaskStatusResponse, error) {
				t.Fatal("invalid body reached Task RPC")
				return nil, nil
			})
			w := httptest.NewRecorder()
			setTaskStatusHandler(client)(w, taskStatusHTTPRequest("200", "500", body, "Bearer token"))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("body %q: %d %s", body, w.Code, w.Body.String())
			}
		})
	}
}
