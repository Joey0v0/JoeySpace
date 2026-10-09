package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	taskpb "github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTaskEnrichmentGroupsOnlyCurrentPageAndMissingNames(t *testing.T) {
	page := []*taskpb.TaskItem{
		{TaskId: 1, TeamId: 100, CreatorId: 5, AssigneeId: 7},
		{TaskId: 2, TeamId: 100, CreatorId: 5, AssigneeId: 5},
		{TaskId: 3, TeamId: 101, CreatorId: 5, AssigneeId: 0},
	}
	client := myTasksFake{list: func(context.Context, *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
		return &taskpb.ListMyTasksResponse{Tasks: page, NextCursor: "another-page"}, nil
	}}
	teamCalls := 0
	members := map[int64]int{}
	names := taskNamesFake{
		teams: func(ctx context.Context, req *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
			assertTaskReadBearer(t, ctx)
			teamCalls++
			if !reflect.DeepEqual(req.TeamIds, []int64{100, 101}) {
				t.Fatalf("team dedupe: %v", req)
			}
			return &userpb.BatchGetMyTeamNamesResponse{Teams: []*userpb.MyTeamName{{TeamId: 100, Name: "A"}}}, nil
		},
		members: func(ctx context.Context, req *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
			assertTaskReadBearer(t, ctx)
			members[req.TeamId]++
			if req.TeamId == 100 {
				if !reflect.DeepEqual(req.UserIds, []int64{5, 7}) {
					t.Fatalf("members A: %v", req)
				}
				return &userpb.BatchGetTeamMemberDisplayNamesResponse{Users: []*userpb.TeamMemberDisplayName{{UserId: 5, DisplayName: "Alice A"}}}, nil
			}
			if req.TeamId != 101 || !reflect.DeepEqual(req.UserIds, []int64{5}) {
				t.Fatalf("members B: %v", req)
			}
			return &userpb.BatchGetTeamMemberDisplayNamesResponse{Users: []*userpb.TeamMemberDisplayName{{UserId: 5, DisplayName: "Alice B"}}}, nil
		}}
	w := httptest.NewRecorder()
	listMyTasksHandler(client, names)(w, taskReadRequest("view=open"))
	var body struct {
		Data struct {
			Tasks []struct {
				TeamName     string `json:"team_name"`
				CreatorName  string `json:"creator_name"`
				AssigneeName string `json:"assignee_name"`
			}
			NextCursor string `json:"next_cursor"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || teamCalls != 1 || !reflect.DeepEqual(members, map[int64]int{100: 1, 101: 1}) || len(body.Data.Tasks) != 3 || body.Data.NextCursor != "another-page" {
		t.Fatalf("calls/response: %d %s %d %v", w.Code, w.Body.String(), teamCalls, members)
	}
	if body.Data.Tasks[0].CreatorName != "Alice A" || body.Data.Tasks[0].AssigneeName != "" || body.Data.Tasks[1].AssigneeName != "Alice A" || body.Data.Tasks[2].CreatorName != "Alice B" || body.Data.Tasks[2].TeamName != "" || body.Data.Tasks[2].AssigneeName != "" {
		t.Fatalf("names: %s", w.Body.String())
	}
}

func TestTaskEnrichmentMaximumPageBound(t *testing.T) {
	for _, sameTeam := range []bool{false, true} {
		page := make([]*taskpb.TaskItem, 50)
		for i := range page {
			team := int64(100 + i)
			if sameTeam {
				team = 100
			}
			page[i] = &taskpb.TaskItem{TaskId: int64(i + 1), TeamId: team, CreatorId: int64(2*i + 1), AssigneeId: int64(2*i + 2)}
		}
		client := myTasksFake{list: func(context.Context, *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
			return &taskpb.ListMyTasksResponse{Tasks: page}, nil
		}}
		teamCalls, memberCalls := 0, 0
		names := taskNamesFake{
			teams: func(ctx context.Context, req *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
				assertTaskReadBearer(t, ctx)
				teamCalls++
				want := 50
				if sameTeam {
					want = 1
				}
				if len(req.TeamIds) != want {
					t.Fatalf("team bound: %v", req)
				}
				return &userpb.BatchGetMyTeamNamesResponse{}, nil
			},
			members: func(ctx context.Context, req *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
				assertTaskReadBearer(t, ctx)
				memberCalls++
				want := 2
				if sameTeam {
					want = 100
				}
				if len(req.UserIds) != want {
					t.Fatalf("member bound: %v", req)
				}
				return &userpb.BatchGetTeamMemberDisplayNamesResponse{}, nil
			}}
		w := httptest.NewRecorder()
		listMyTasksHandler(client, names)(w, taskReadRequest("view=open&limit=50"))
		want := 50
		if sameTeam {
			want = 1
		}
		if w.Code != 200 || teamCalls != 1 || memberCalls != want {
			t.Fatalf("bound: %d %s calls %d/%d", w.Code, w.Body.String(), teamCalls, memberCalls)
		}
	}
}

func TestTaskEnrichmentRejectsInvalidUserResponses(t *testing.T) {
	teams := map[string]*userpb.BatchGetMyTeamNamesResponse{
		"nil": nil, "nil item": {Teams: []*userpb.MyTeamName{nil}},
		"zero":      {Teams: []*userpb.MyTeamName{{TeamId: 0, Name: "name"}}},
		"negative":  {Teams: []*userpb.MyTeamName{{TeamId: -1, Name: "name"}}},
		"extra":     {Teams: []*userpb.MyTeamName{{TeamId: 101, Name: "name"}}},
		"duplicate": {Teams: []*userpb.MyTeamName{{TeamId: 100, Name: "name"}, {TeamId: 100, Name: "name"}}},
		"empty":     {Teams: []*userpb.MyTeamName{{TeamId: 100}}},
	}
	members := map[string]*userpb.BatchGetTeamMemberDisplayNamesResponse{
		"nil": nil, "nil item": {Users: []*userpb.TeamMemberDisplayName{nil}},
		"zero":      {Users: []*userpb.TeamMemberDisplayName{{UserId: 0, DisplayName: "name"}}},
		"negative":  {Users: []*userpb.TeamMemberDisplayName{{UserId: -1, DisplayName: "name"}}},
		"extra":     {Users: []*userpb.TeamMemberDisplayName{{UserId: 6, DisplayName: "name"}}},
		"duplicate": {Users: []*userpb.TeamMemberDisplayName{{UserId: 5, DisplayName: "name"}, {UserId: 5, DisplayName: "name"}}},
		"empty":     {Users: []*userpb.TeamMemberDisplayName{{UserId: 5}}},
	}
	for stage := 0; stage < 2; stage++ {
		cases := teams
		if stage == 1 {
			cases = map[string]*userpb.BatchGetMyTeamNamesResponse{}
			for name := range members {
				cases[name] = nil
			}
		}
		for name := range cases {
			t.Run(fmt.Sprintf("%d/%s", stage, name), func(t *testing.T) {
				client := myTasksFake{list: func(context.Context, *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
					return &taskpb.ListMyTasksResponse{Tasks: []*taskpb.TaskItem{validTaskReadItem()}}, nil
				}}
				names := taskNamesFake{
					teams: func(context.Context, *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
						if stage == 0 {
							return teams[name], nil
						}
						return &userpb.BatchGetMyTeamNamesResponse{}, nil
					},
					members: func(context.Context, *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
						if stage == 0 {
							t.Fatal("invalid team response reached member enrichment")
						}
						return members[name], nil
					},
				}
				w := httptest.NewRecorder()
				listMyTasksHandler(client, names)(w, taskReadRequest("view=open"))
				if w.Code != 502 || strings.Contains(w.Body.String(), `"data"`) {
					t.Fatalf("invalid User: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestTaskEnrichmentFailuresRemainErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Aborted, 409}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502},
	} {
		for _, memberStage := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/%v", tc.code, memberStage), func(t *testing.T) {
				client := myTasksFake{list: func(context.Context, *taskpb.ListMyTasksRequest) (*taskpb.ListMyTasksResponse, error) {
					return &taskpb.ListMyTasksResponse{Tasks: []*taskpb.TaskItem{validTaskReadItem()}}, nil
				}}
				private := status.Error(tc.code, "private User detail")
				names := taskNamesFake{
					teams: func(context.Context, *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
						if !memberStage {
							return nil, private
						}
						return &userpb.BatchGetMyTeamNamesResponse{}, nil
					},
					members: func(context.Context, *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
						if !memberStage {
							t.Fatal("team error reached members")
						}
						return nil, private
					},
				}
				w := httptest.NewRecorder()
				listMyTasksHandler(client, names)(w, taskReadRequest("view=open"))
				if w.Code != tc.want || strings.Contains(w.Body.String(), "private User detail") || strings.Contains(w.Body.String(), `"data"`) {
					t.Fatalf("failure: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestTaskDetailEnrichmentMakesOneCallPerKind(t *testing.T) {
	teamCalls, memberCalls := 0, 0
	client := myTasksFake{detail: func(context.Context, *taskpb.GetTaskRequest) (*taskpb.GetTaskResponse, error) {
		return &taskpb.GetTaskResponse{Task: &taskpb.TaskItem{TaskId: 1, TeamId: 100, CreatorId: 5, AssigneeId: 5}}, nil
	}}
	names := taskNamesFake{
		teams: func(ctx context.Context, req *userpb.BatchGetMyTeamNamesRequest) (*userpb.BatchGetMyTeamNamesResponse, error) {
			assertTaskReadBearer(t, ctx)
			teamCalls++
			if !reflect.DeepEqual(req.TeamIds, []int64{100}) {
				t.Fatalf("team: %v", req)
			}
			return &userpb.BatchGetMyTeamNamesResponse{Teams: []*userpb.MyTeamName{{TeamId: 100, Name: "A"}}}, nil
		},
		members: func(ctx context.Context, req *userpb.BatchGetTeamMemberDisplayNamesRequest) (*userpb.BatchGetTeamMemberDisplayNamesResponse, error) {
			assertTaskReadBearer(t, ctx)
			memberCalls++
			if req.TeamId != 100 || !reflect.DeepEqual(req.UserIds, []int64{5}) {
				t.Fatalf("members: %v", req)
			}
			return &userpb.BatchGetTeamMemberDisplayNamesResponse{Users: []*userpb.TeamMemberDisplayName{{UserId: 5, DisplayName: "Alice"}}}, nil
		},
	}
	w := httptest.NewRecorder()
	getTaskDetailHandler(client, names)(w, taskDetailReadRequest("100", "1", ""))
	if w.Code != 200 || teamCalls != 1 || memberCalls != 1 || !strings.Contains(w.Body.String(), `"creator_name":"Alice"`) || !strings.Contains(w.Body.String(), `"assignee_name":"Alice"`) {
		t.Fatalf("detail: %d %s", w.Code, w.Body.String())
	}
}
