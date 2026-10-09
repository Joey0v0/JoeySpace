package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type myTasksFake struct {
	list   func(context.Context, *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error)
	detail func(context.Context, *taskpb.GetTaskRequest) (*taskpb.GetTaskResponse, error)
}

func (f myTasksFake) ListMyTasks(ctx context.Context, req *taskpb.ListMyTasksRequest, _ ...grpc.CallOption) (*taskpb.ListMyTasksResponse, error) {
	return f.list(ctx, req)
}
func (f myTasksFake) GetTask(ctx context.Context, req *taskpb.GetTaskRequest, _ ...grpc.CallOption) (*taskpb.GetTaskResponse, error) {
	return f.detail(ctx, req)
}

type taskNamesFake struct {
	teams   func(context.Context, *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error)
	members func(context.Context, *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error)
}

func (f taskNamesFake) BatchGetMyTeamNames(ctx context.Context, req *userpb.BatchGetMyTeamNamesRequest, _ ...grpc.CallOption) (*userpb.BatchGetMyTeamNamesResponse, error) {
	if f.teams == nil {
		return &userpb.BatchGetMyTeamNamesResponse{}, nil
	}
	return f.teams(ctx, req)
}
func (f taskNamesFake) BatchGetTeamMemberDisplayNames(ctx context.Context, req *userpb.BatchGetTeamMemberDisplayNamesRequest, _ ...grpc.CallOption) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
	if f.members == nil {
		return &userpb.BatchGetTeamMemberDisplayNamesResponse{}, nil
	}
	return f.members(ctx, req)
}

func taskReadRequest(query string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks", nil)
	r.URL.RawQuery = query
	r.Header.Set("Authorization", " bearer   test-token ")
	ctx := metadata.NewIncomingContext(r.Context(), metadata.Pairs("private-incoming", "do-not-forward", "authorization", "Bearer forged"))
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("private-outgoing", "do-not-forward", "authorization", "Bearer forged"))
	return r.WithContext(ctx)
}
func assertTaskReadBearer(t *testing.T, ctx context.Context) {
	t.Helper()
	md, _ := metadata.FromOutgoingContext(ctx)
	if !reflect.DeepEqual(md, metadata.Pairs("authorization", "Bearer test-token")) {
		t.Fatalf("outgoing metadata: %v", md)
	}
}
func noTaskEnrichment(t *testing.T) taskNamesFake {
	return taskNamesFake{
		teams: func(context.Context, *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
			t.Fatal("unexpected User teams call")
			return nil, nil
		},
		members: func(context.Context, *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
			t.Fatal("unexpected User members call")
			return nil, nil
		},
	}
}
func validTaskReadItem() *taskpb.TaskItem {
	return &taskpb.TaskItem{TaskId: 1, TeamId: 100, CreatorId: 5}
}

func TestMyTasksHTTPFiltersCursorBearerAndExactStrings(t *testing.T) {
	const base int64 = 9007199254740993
	cursor := "opaque+/= ?"
	tasks := myTasksFake{list: func(ctx context.Context, req *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
		assertTaskReadBearer(t, ctx)
		if req.View != 1 || req.TeamId != base || req.Limit != 50 || req.Cursor != cursor {
			t.Fatalf("request: %v", req)
		}
		return &taskpb.ListMyTasksResponse{Tasks: []*taskpb.TaskItem{{TaskId: base + 2, TeamId: base, CreatorId: base + 4, AssigneeId: base + 6, Status: 2, Title: "title", Description: "body", SourceGroupId: base + 8, SourceMessageId: base + 10, DueAtUnixMs: base + 12}}, NextCursor: cursor}, nil
	}}
	teamCalls, memberCalls := 0, 0
	names := taskNamesFake{
		teams: func(ctx context.Context, req *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
			assertTaskReadBearer(t, ctx)
			teamCalls++
			if !reflect.DeepEqual(req.TeamIds, []int64{base}) {
				t.Fatalf("teams: %v", req)
			}
			return &userpb.BatchGetMyTeamNamesResponse{Teams: []*userpb.MyTeamName{{TeamId: base, Name: "team"}}}, nil
		},
		members: func(ctx context.Context, req *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
			assertTaskReadBearer(t, ctx)
			memberCalls++
			if req.TeamId != base || !reflect.DeepEqual(req.UserIds, []int64{base + 4, base + 6}) {
				t.Fatalf("members: %v", req)
			}
			return &userpb.BatchGetTeamMemberDisplayNamesResponse{Users: []*userpb.TeamMemberDisplayName{{UserId: base + 4, DisplayName: "creator"}, {UserId: base + 6, DisplayName: "assignee"}}}, nil
		}}
	w := httptest.NewRecorder()
	listMyTasksHandler(tasks, names)(w, taskReadRequest("view=completed&team_id=9007199254740993&limit=50&cursor="+url.QueryEscape(cursor)))
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	expected := map[string]any{"code": float64(0), "msg": "success", "data": map[string]any{"next_cursor": cursor, "tasks": []any{map[string]any{
		"task_id": "9007199254740995", "team_id": "9007199254740993", "team_name": "team", "title": "title", "description": "body",
		"creator_id": "9007199254740997", "creator_name": "creator", "assignee_id": "9007199254740999", "assignee_name": "assignee", "status": float64(2),
		"source_group_id": "9007199254741001", "source_message_id": "9007199254741003", "due_at_unix_ms": "9007199254741005",
	}}}}
	if w.Code != 200 || !reflect.DeepEqual(got, expected) || teamCalls != 1 || memberCalls != 1 {
		t.Fatalf("response: %d %s; calls %d/%d", w.Code, w.Body.String(), teamCalls, memberCalls)
	}
}

func TestMyTasksHTTPDefaultsAndEmptyArray(t *testing.T) {
	for _, query := range []string{"view=open", "view=open&team_id=0"} {
		client := myTasksFake{list: func(ctx context.Context, req *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
			assertTaskReadBearer(t, ctx)
			if req.View != 0 || req.TeamId != 0 || req.Limit != 0 || req.Cursor != "" {
				t.Fatalf("defaults: %v", req)
			}
			return &taskpb.ListMyTasksResponse{}, nil
		}}
		w := httptest.NewRecorder()
		listMyTasksHandler(client, noTaskEnrichment(t))(w, taskReadRequest(query))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"tasks":[]`) || !strings.Contains(w.Body.String(), `"next_cursor":""`) {
			t.Fatalf("empty: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestMyTasksHTTPInvalidQueriesBeforeRPC(t *testing.T) {
	client := myTasksFake{list: func(context.Context, *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
		t.Fatal("invalid request reached Task")
		return nil, nil
	}}
	queries := []string{"", "view=", "view=other", "view=OPEN", "view=open&view=completed", "view=open&other=1", "view=open&user_id=1", "view=open&team_id=", "view=open&team_id=-1", "view=open&team_id=%2B1", "view=open&team_id=1.0", "view=open&team_id=9223372036854775808", "view=open&team_id=1&team_id=2", "view=open&limit=", "view=open&limit=0", "view=open&limit=-1", "view=open&limit=51", "view=open&limit=%2B1", "view=open&limit=1.0", "view=open&limit=1&limit=1", "view=open&cursor=", "view=open&cursor=x&cursor=y", "view=open&cursor=" + strings.Repeat("x", 2049), "view=open&cursor=%ZZ", "view=open;limit=1"}
	for _, query := range queries {
		t.Run(query, func(t *testing.T) {
			w := httptest.NewRecorder()
			listMyTasksHandler(client, noTaskEnrichment(t))(w, taskReadRequest(query))
			if w.Code != 400 {
				t.Fatalf("invalid query: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMyTasksHTTPAuthBeforeRPC(t *testing.T) {
	client := myTasksFake{list: func(context.Context, *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
		t.Fatal("invalid auth reached Task")
		return nil, nil
	}}
	for _, headers := range [][]string{nil, {"Basic token"}, {"Bearer"}, {"Bearer a b"}, {"Bearer a", "Bearer b"}} {
		r := taskReadRequest("view=open")
		r.Header.Del("Authorization")
		for _, value := range headers {
			r.Header.Add("Authorization", value)
		}
		w := httptest.NewRecorder()
		listMyTasksHandler(client, noTaskEnrichment(t))(w, r)
		if w.Code != 401 {
			t.Fatalf("auth: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestMyTasksHTTPTaskFailuresSkipEnrichment(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Aborted, 409}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}, {codes.Unknown, 502}, {codes.Canceled, 502},
	} {
		t.Run(tc.code.String(), func(t *testing.T) {
			client := myTasksFake{list: func(context.Context, *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
				return nil, status.Error(tc.code, "private database detail")
			}}
			w := httptest.NewRecorder()
			listMyTasksHandler(client, noTaskEnrichment(t))(w, taskReadRequest("view=open"))
			if w.Code != tc.want || strings.Contains(w.Body.String(), "private database detail") || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("failure: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestMyTasksHTTPMalformedTaskResponsesSkipEnrichment(t *testing.T) {
	responses := map[string]*taskpb.ListMyTasksResponse{"nil response": nil, "nil item": {Tasks: []*taskpb.TaskItem{nil}}}
	mutations := map[string]func(*taskpb.TaskItem){
		"zero task": func(x *taskpb.TaskItem) { x.TaskId = 0 }, "negative task": func(x *taskpb.TaskItem) { x.TaskId = -1 },
		"zero team": func(x *taskpb.TaskItem) { x.TeamId = 0 }, "negative team": func(x *taskpb.TaskItem) { x.TeamId = -1 },
		"zero creator": func(x *taskpb.TaskItem) { x.CreatorId = 0 }, "negative creator": func(x *taskpb.TaskItem) { x.CreatorId = -1 },
		"negative assignee": func(x *taskpb.TaskItem) { x.AssigneeId = -1 }, "negative status": func(x *taskpb.TaskItem) { x.Status = -1 }, "invalid status": func(x *taskpb.TaskItem) { x.Status = 3 },
		"negative due":      func(x *taskpb.TaskItem) { x.DueAtUnixMs = -1 },
		"source only group": func(x *taskpb.TaskItem) { x.SourceGroupId = 1 }, "source only message": func(x *taskpb.TaskItem) { x.SourceMessageId = 1 },
		"negative source": func(x *taskpb.TaskItem) { x.SourceGroupId = -1; x.SourceMessageId = -1 },
	}
	for name, mutate := range mutations {
		item := validTaskReadItem()
		mutate(item)
		responses[name] = &taskpb.ListMyTasksResponse{Tasks: []*taskpb.TaskItem{item}}
	}
	responses["duplicate task"] = &taskpb.ListMyTasksResponse{Tasks: []*taskpb.TaskItem{validTaskReadItem(), validTaskReadItem()}}
	large := make([]*taskpb.TaskItem, 51)
	for i := range large {
		large[i] = validTaskReadItem()
		large[i].TaskId = int64(i + 1)
	}
	responses["oversized page"] = &taskpb.ListMyTasksResponse{Tasks: large}
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			client := myTasksFake{list: func(context.Context, *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
				return response, nil
			}}
			w := httptest.NewRecorder()
			listMyTasksHandler(client, noTaskEnrichment(t))(w, taskReadRequest("view=open"))
			if w.Code != 502 || strings.Contains(w.Body.String(), `"data"`) {
				t.Fatalf("malformed: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
