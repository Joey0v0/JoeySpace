package main

import (
	"context"
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

type teamGroupJoinClient struct {
	pb.IMClient
	call func(context.Context, *pb.JoinTeamGroupRequest) (*pb.JoinTeamGroupResponse, error)
}

func (c teamGroupJoinClient) JoinTeamGroup(ctx context.Context, req *pb.JoinTeamGroupRequest, _ ...grpc.CallOption) (*pb.JoinTeamGroupResponse, error) {
	return c.call(ctx, req)
}

func teamGroupJoinRequest(teamID, groupID, token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/teams/"+teamID+"/groups/"+groupID+"/join", nil)
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID, "group_id": groupID})
	if token != "" {
		r.Header.Set("Authorization", token)
	}
	return r
}

func TestJoinTeamGroupHTTPRejectsInvalidRequest(t *testing.T) {
	client := teamGroupJoinClient{call: func(context.Context, *pb.JoinTeamGroupRequest) (*pb.JoinTeamGroupResponse, error) {
		t.Fatal("invalid request reached IM RPC")
		return nil, nil
	}}
	for _, tc := range []struct {
		team, group, token string
		want               int
	}{
		{"200", "300", "", 401},
		{"200", "300", "Basic token", 401},
		{"0", "300", "Bearer token", 400},
		{"200", "bad", "Bearer token", 400},
		{"200", "0", "Bearer token", 400},
	} {
		w := httptest.NewRecorder()
		joinTeamGroupHandler(client)(w, teamGroupJoinRequest(tc.team, tc.group, tc.token))
		if w.Code != tc.want {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
}

func TestJoinTeamGroupHTTPForwardsTokenAndIDs(t *testing.T) {
	client := teamGroupJoinClient{call: func(ctx context.Context, req *pb.JoinTeamGroupRequest) (*pb.JoinTeamGroupResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetGroupId() != 9007199254740995 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
			t.Fatalf("wrong RPC request: %v %v", req, md)
		}
		return &pb.JoinTeamGroupResponse{}, nil
	}}
	w := httptest.NewRecorder()
	joinTeamGroupHandler(client)(w, teamGroupJoinRequest("9007199254740993", "9007199254740995", "Bearer test-token"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatalf("join response: %d %s", w.Code, w.Body.String())
	}
}

func TestJoinTeamGroupHTTPMapsErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{{codes.InvalidArgument, 400}, {codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.NotFound, 404}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504}, {codes.Internal, 502}} {
		client := teamGroupJoinClient{call: func(context.Context, *pb.JoinTeamGroupRequest) (*pb.JoinTeamGroupResponse, error) {
			return nil, status.Error(tc.code, "private detail")
		}}
		w := httptest.NewRecorder()
		joinTeamGroupHandler(client)(w, teamGroupJoinRequest("200", "300", "Bearer token"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private detail") {
			t.Fatalf("RPC %v: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
