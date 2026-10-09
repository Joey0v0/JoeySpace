package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func taskDetailReadRequest(teamID, taskID, query string) *http.Request {
	r := taskReadRequest(query)
	return pathvar.WithVars(r, map[string]string{"team_id": teamID, "task_id": taskID})
}

func TestTaskDetailHTTPForwardingStringIDsAndPermission(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cannot update", true: "can update"}[allowed], func(t *testing.T) {
			const base int64 = 9007199254740993
			client := myTasksFake{detail: func(ctx context.Context, req *taskpb.GetTaskRequest) (*taskpb.GetTaskResponse, error) {
				assertTaskReadBearer(t, ctx)
				if req.TeamId != base || req.TaskId != base+2 {
					t.Fatalf("request: %v", req)
				}
				return &taskpb.GetTaskResponse{Task: &taskpb.TaskItem{TeamId: base, TaskId: base + 2, CreatorId: base + 4, DueAtUnixMs: base + 6, Status: 1}, CanUpdateStatus: allowed}, nil
			}}
			w := httptest.NewRecorder()
			getTaskDetailHandler(client, taskNamesFake{})(w, taskDetailReadRequest("9007199254740993", "9007199254740995", ""))
			var got map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			expected := map[string]any{"code": float64(0), "msg": "success", "data": map[string]any{"can_update_status": allowed, "task": map[string]any{
				"team_id": "9007199254740993", "task_id": "9007199254740995", "team_name": "", "title": "", "description": "",
				"creator_id": "9007199254740997", "creator_name": "", "assignee_id": "0", "assignee_name": "", "status": float64(1),
				"source_group_id": "0", "source_message_id": "0", "due_at_unix_ms": "9007199254740999",
			}}}
			if w.Code != 200 || !reflect.DeepEqual(got, expected) {
				t.Fatalf("detail: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestTaskDetailHTTPRejectsInvalidInputBeforeRPC(t *testing.T) {
	client := myTasksFake{detail: func(context.Context, *taskpb.GetTaskRequest) (*taskpb.GetTaskResponse, error) {
		t.Fatal("invalid detail reached Task")
		return nil, nil
	}}
	for _, tc := range []struct{ team, task, query string }{
		{"0", "1", ""}, {"-1", "1", ""}, {"+1", "1", ""}, {"1", "0", ""}, {"1", "-1", ""}, {"1", "+1", ""}, {"x", "1", ""}, {"1", "1.0", ""}, {"1", "9223372036854775808", ""}, {"1", "1", "limit=1"}, {"1", "1", "other="}, {"1", "1", "%ZZ=x"},
	} {
		w := httptest.NewRecorder()
		getTaskDetailHandler(client, noTaskEnrichment(t))(w, taskDetailReadRequest(tc.team, tc.task, tc.query))
		if w.Code != 400 {
			t.Fatalf("input %v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	r := taskDetailReadRequest("100", "1", "")
	r.Header.Del("Authorization")
	w := httptest.NewRecorder()
	getTaskDetailHandler(client, noTaskEnrichment(t))(w, r)
	if w.Code != 401 {
		t.Fatalf("missing auth: %d", w.Code)
	}
}

func TestTaskDetailHTTPMalformedOrMismatchedTaskSkipsEnrichment(t *testing.T) {
	bad := validTaskReadItem()
	bad.TaskId = 2
	wrongTeam := validTaskReadItem()
	wrongTeam.TeamId = 101
	wrongDue := validTaskReadItem()
	wrongDue.DueAtUnixMs = -1
	for _, res := range []*taskpb.GetTaskResponse{nil, {}, {Task: bad}, {Task: wrongTeam}, {Task: wrongDue}} {
		client := myTasksFake{detail: func(context.Context, *taskpb.GetTaskRequest) (*taskpb.GetTaskResponse, error) { return res, nil }}
		w := httptest.NewRecorder()
		getTaskDetailHandler(client, noTaskEnrichment(t))(w, taskDetailReadRequest("100", "1", ""))
		if w.Code != 502 || strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("malformed: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestTaskDetailHTTPMapsDownstreamErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Aborted, 409}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502},
	} {
		client := myTasksFake{detail: func(context.Context, *taskpb.GetTaskRequest) (*taskpb.GetTaskResponse, error) {
			return nil, status.Error(tc.code, "private detail")
		}}
		w := httptest.NewRecorder()
		getTaskDetailHandler(client, noTaskEnrichment(t))(w, taskDetailReadRequest("100", "1", ""))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private detail") {
			t.Fatalf("%v: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
