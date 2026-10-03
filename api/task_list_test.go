package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/task/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type taskListFunc func(context.Context, *pb.ListTeamTasksRequest) (*pb.ListTeamTasksResponse, error)

func (f taskListFunc) ListTeamTasks(ctx context.Context, req *pb.ListTeamTasksRequest, _ ...grpc.CallOption) (*pb.ListTeamTasksResponse, error) {
	return f(ctx, req)
}

func taskListHTTPRequest(teamID, query string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/teams/"+teamID+"/tasks"+query, nil)
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID})
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func TestListTeamTasksHTTPForwardsCursorAndStringIDs(t *testing.T) {
	client := taskListFunc(func(ctx context.Context, req *pb.ListTeamTasksRequest) (*pb.ListTeamTasksResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetAfterTaskId() != 9007199254740995 || req.GetLimit() != 2 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
			t.Fatalf("wrong RPC request or token: %v, %v", req, md)
		}
		return &pb.ListTeamTasksResponse{
			Tasks:           []*pb.TaskItem{{TaskId: 9007199254740997, Title: "Planning", Description: "Check", CreatorId: 9007199254740999, AssigneeId: 9007199254741001, Status: 1, SourceGroupId: 9007199254741003, SourceMessageId: 9007199254741005, DueAtUnixMs: 1790874000000}},
			NextAfterTaskId: 9007199254740997,
		}, nil
	})
	w := httptest.NewRecorder()
	listTeamTasksHandler(client)(w, taskListHTTPRequest("9007199254740993", "?after_task_id=9007199254740995&limit=2", "Bearer test-token"))
	var body struct {
		Code int `json:"code"`
		Data struct {
			Tasks []struct {
				TaskID          string `json:"task_id"`
				CreatorID       string `json:"creator_id"`
				AssigneeID      string `json:"assignee_id"`
				Status          int32  `json:"status"`
				SourceGroupID   string `json:"source_group_id"`
				SourceMessageID string `json:"source_message_id"`
				DueAtUnixMs     int64  `json:"due_at_unix_ms"`
			} `json:"tasks"`
			NextAfterTaskID string `json:"next_after_task_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || body.Code != 0 || len(body.Data.Tasks) != 1 || body.Data.Tasks[0].TaskID != "9007199254740997" || body.Data.Tasks[0].CreatorID != "9007199254740999" || body.Data.Tasks[0].AssigneeID != "9007199254741001" || body.Data.Tasks[0].SourceGroupID != "9007199254741003" || body.Data.Tasks[0].SourceMessageID != "9007199254741005" || body.Data.Tasks[0].DueAtUnixMs != 1790874000000 || body.Data.Tasks[0].Status != 1 || body.Data.NextAfterTaskID != "9007199254740997" {
		t.Fatalf("list response: %d %s", w.Code, w.Body.String())
	}
}

func TestListTeamTasksHTTPWithoutDueTimeReturnsZero(t *testing.T) {
	client := taskListFunc(func(context.Context, *pb.ListTeamTasksRequest) (*pb.ListTeamTasksResponse, error) {
		return &pb.ListTeamTasksResponse{Tasks: []*pb.TaskItem{{TaskId: 1}}}, nil
	})
	w := httptest.NewRecorder()
	listTeamTasksHandler(client)(w, taskListHTTPRequest("200", "", "Bearer token"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"due_at_unix_ms":0`) {
		t.Fatalf("unset due time: %d %s", w.Code, w.Body.String())
	}
}

func TestListTeamTasksHTTPRejectsInvalidInputBeforeRPC(t *testing.T) {
	client := taskListFunc(func(context.Context, *pb.ListTeamTasksRequest) (*pb.ListTeamTasksResponse, error) {
		t.Fatal("invalid list reached Task RPC")
		return nil, nil
	})
	for _, tc := range []struct {
		teamID, query string
		headers       []string
		want          int
	}{
		{"200", "", nil, 401},
		{"200", "", []string{"Bearer a", "Bearer b"}, 401},
		{"200", "", []string{"Basic token"}, 401},
		{"0", "", []string{"Bearer token"}, 400},
		{"bad", "", []string{"Bearer token"}, 400},
		{"200", "?after_task_id=-1", []string{"Bearer token"}, 400},
		{"200", "?after_task_id=abc", []string{"Bearer token"}, 400},
		{"200", "?after_task_id=1&after_task_id=2", []string{"Bearer token"}, 400},
		{"200", "?limit=0", []string{"Bearer token"}, 400},
		{"200", "?limit=101", []string{"Bearer token"}, 400},
		{"200", "?unknown=1", []string{"Bearer token"}, 400},
	} {
		w := httptest.NewRecorder()
		listTeamTasksHandler(client)(w, taskListHTTPRequest(tc.teamID, tc.query, tc.headers...))
		if w.Code != tc.want {
			t.Fatalf("team=%q query=%q: got %d %s", tc.teamID, tc.query, w.Code, w.Body.String())
		}
	}
}

func TestListTeamTasksHTTPReturnsEmptyArrayAndMapsFailures(t *testing.T) {
	client := taskListFunc(func(context.Context, *pb.ListTeamTasksRequest) (*pb.ListTeamTasksResponse, error) {
		return &pb.ListTeamTasksResponse{}, nil
	})
	w := httptest.NewRecorder()
	listTeamTasksHandler(client)(w, taskListHTTPRequest("200", "", "Bearer token"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tasks":[]`) || !strings.Contains(w.Body.String(), `"next_after_task_id":"0"`) {
		t.Fatalf("empty list: %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, 400},
		{codes.Unauthenticated, 401},
		{codes.PermissionDenied, 403},
		{codes.Unavailable, 503},
		{codes.DeadlineExceeded, 504},
		{codes.Internal, 502},
	} {
		client := taskListFunc(func(context.Context, *pb.ListTeamTasksRequest) (*pb.ListTeamTasksResponse, error) {
			return nil, status.Error(tc.code, "private database detail")
		})
		w := httptest.NewRecorder()
		listTeamTasksHandler(client)(w, taskListHTTPRequest("200", "", "Bearer token"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private database detail") || strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("RPC %v: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
