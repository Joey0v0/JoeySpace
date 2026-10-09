package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type teamGroupListClient struct {
	pb.IMClient
	call func(context.Context, *pb.ListTeamGroupsRequest) (*pb.ListTeamGroupsResponse, error)
}

func (c teamGroupListClient) ListTeamGroups(ctx context.Context, req *pb.ListTeamGroupsRequest, _ ...grpc.CallOption) (*pb.ListTeamGroupsResponse, error) {
	return c.call(ctx, req)
}

func teamGroupListRequest(teamID, query, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/teams/"+teamID+"/groups"+query, nil)
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID})
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	return r
}

func TestListTeamGroupsHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := teamGroupListClient{call: func(context.Context, *pb.ListTeamGroupsRequest) (*pb.ListTeamGroupsResponse, error) {
		t.Fatal("invalid request reached IM RPC")
		return nil, nil
	}}
	for _, tc := range []struct {
		teamID, query, token string
		want                 int
	}{
		{"200", "", "", 401},
		{"0", "", "Bearer token", 400},
		{"bad", "", "Bearer token", 400},
		{"200", "?limit=101", "Bearer token", 400},
		{"200", "?after_group_id=-1", "Bearer token", 400},
		{"200", "?limit=2&limit=3", "Bearer token", 400},
		{"200", "?unknown=1", "Bearer token", 400},
	} {
		w := httptest.NewRecorder()
		listTeamGroupsHandler(client)(w, teamGroupListRequest(tc.teamID, tc.query, tc.token))
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}

func TestListTeamGroupsHTTPForwardsAndPreservesIDs(t *testing.T) {
	client := teamGroupListClient{call: func(ctx context.Context, req *pb.ListTeamGroupsRequest) (*pb.ListTeamGroupsResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetAfterGroupId() != 9 || req.GetLimit() != 2 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
			t.Fatalf("unexpected RPC request: %v, %v", req, md)
		}
		return &pb.ListTeamGroupsResponse{Groups: []*pb.TeamGroup{{GroupId: 9007199254740995, Name: "Planning", OwnerId: 9007199254740997, Joined: true}}, NextAfterGroupId: 9007199254740995}, nil
	}}
	w := httptest.NewRecorder()
	listTeamGroupsHandler(client)(w, teamGroupListRequest("9007199254740993", "?after_group_id=9&limit=2", "Bearer test-token"))
	var result struct {
		Data struct {
			Groups []struct {
				GroupID string `json:"group_id"`
				OwnerID string `json:"owner_id"`
				Joined  bool   `json:"joined"`
			} `json:"groups"`
			Next string `json:"next_after_group_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(result.Data.Groups) != 1 || result.Data.Groups[0].GroupID != "9007199254740995" || result.Data.Groups[0].OwnerID != "9007199254740997" || !result.Data.Groups[0].Joined || result.Data.Next != "9007199254740995" {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}

func TestListTeamGroupsHTTPMapsErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.PermissionDenied, 403}, {codes.Unauthenticated, 401}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		client := teamGroupListClient{call: func(context.Context, *pb.ListTeamGroupsRequest) (*pb.ListTeamGroupsResponse, error) {
			return nil, status.Error(tc.code, "private detail")
		}}
		w := httptest.NewRecorder()
		listTeamGroupsHandler(client)(w, teamGroupListRequest("200", "", "Bearer token"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private detail") {
			t.Fatalf("RPC %v: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
