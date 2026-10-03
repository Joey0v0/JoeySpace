package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/user/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type memberListClient struct {
	pb.UserClient
	call func(context.Context, *pb.ListTeamMembersRequest) (*pb.ListTeamMembersResponse, error)
}

func (c memberListClient) ListTeamMembers(ctx context.Context, req *pb.ListTeamMembersRequest, _ ...grpc.CallOption) (*pb.ListTeamMembersResponse, error) {
	return c.call(ctx, req)
}

func memberListRequest(teamID, query string, headers ...string) *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/teams/"+teamID+"/members"+query, nil)
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID})
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func TestMemberListHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := memberListClient{call: func(context.Context, *pb.ListTeamMembersRequest) (*pb.ListTeamMembersResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	}}
	for _, tc := range []struct {
		teamID, query string
		headers       []string
		want          int
	}{
		{"100", "", nil, 401},
		{"100", "", []string{"Bearer a", "Bearer b"}, 401},
		{"0", "", []string{"Bearer token"}, 400},
		{"bad", "", []string{"Bearer token"}, 400},
		{"100", "?limit=101", []string{"Bearer token"}, 400},
		{"100", "?limit=0", []string{"Bearer token"}, 400},
		{"100", "?after_user_id=-1", []string{"Bearer token"}, 400},
		{"100", "?limit=1&limit=2", []string{"Bearer token"}, 400},
		{"100", "?unknown=1", []string{"Bearer token"}, 400},
		{"100", "?limit=%zz", []string{"Bearer token"}, 400},
	} {
		w := httptest.NewRecorder()
		listTeamMembersHandler(client)(w, memberListRequest(tc.teamID, tc.query, tc.headers...))
		if w.Code != tc.want {
			t.Fatalf("team %q, query %q: got %d %s", tc.teamID, tc.query, w.Code, w.Body.String())
		}
	}
}

func TestMemberListHTTPForwardsRequestAndReturnsPage(t *testing.T) {
	client := memberListClient{call: func(ctx context.Context, req *pb.ListTeamMembersRequest) (*pb.ListTeamMembersResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetAfterUserId() != 9007199254740995 || req.GetLimit() != 2 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
			t.Fatalf("wrong RPC request: %v", req)
		}
		return &pb.ListTeamMembersResponse{Members: []*pb.TeamMember{
			{UserId: 9007199254740997, Username: "alice", Nickname: "Alice", Role: 2},
		}, NextAfterUserId: 9007199254740997}, nil
	}}
	w := httptest.NewRecorder()
	listTeamMembersHandler(client)(w, memberListRequest("9007199254740993", "?after_user_id=9007199254740995&limit=2", "Bearer test-token"))
	var result struct {
		Code int `json:"code"`
		Data struct {
			Members []struct {
				UserID string `json:"user_id"`
				Role   int32  `json:"role"`
			} `json:"members"`
			Next string `json:"next_after_user_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Code != 0 || len(result.Data.Members) != 1 || result.Data.Members[0].UserID != "9007199254740997" || result.Data.Members[0].Role != 2 || result.Data.Next != "9007199254740997" || strings.Contains(w.Body.String(), "test-token") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestMemberListHTTPDefaultPageIsEmptyArray(t *testing.T) {
	client := memberListClient{call: func(_ context.Context, req *pb.ListTeamMembersRequest) (*pb.ListTeamMembersResponse, error) {
		if req.GetAfterUserId() != 0 || req.GetLimit() != 0 {
			t.Fatalf("unexpected default pagination: %v", req)
		}
		return &pb.ListTeamMembersResponse{}, nil
	}}
	w := httptest.NewRecorder()
	listTeamMembersHandler(client)(w, memberListRequest("100", "", "Bearer token"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"members":[]`) || !strings.Contains(w.Body.String(), `"next_after_user_id":"0"`) {
		t.Fatalf("empty page: %d %s", w.Code, w.Body.String())
	}
}

func TestMemberListHTTPMapsRPCErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, 400},
		{codes.Unauthenticated, 401},
		{codes.PermissionDenied, 403},
		{codes.NotFound, 404},
		{codes.Unavailable, 503},
		{codes.DeadlineExceeded, 504},
		{codes.Internal, 502},
	} {
		client := memberListClient{call: func(context.Context, *pb.ListTeamMembersRequest) (*pb.ListTeamMembersResponse, error) {
			return nil, status.Error(tc.code, "private database detail")
		}}
		w := httptest.NewRecorder()
		listTeamMembersHandler(client)(w, memberListRequest("100", "", "Bearer token"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private database detail") || strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("RPC %v: got %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
