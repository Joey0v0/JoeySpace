package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type teamClient struct {
	pb.UserClient
	call func(context.Context, *pb.CreateTeamRequest) (*pb.CreateTeamResponse, error)
}

func (c teamClient) CreateTeam(ctx context.Context, req *pb.CreateTeamRequest, _ ...grpc.CallOption) (*pb.CreateTeamResponse, error) {
	return c.call(ctx, req)
}

func TestCreateTeamHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := teamClient{call: func(context.Context, *pb.CreateTeamRequest) (*pb.CreateTeamResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	}}
	for _, tc := range []struct {
		body   string
		header []string
		want   int
	}{
		{`{"name":"team"}`, nil, 401},
		{`{"name":"team"}`, []string{"Basic token"}, 401},
		{`{"name":"team"}`, []string{"Bearer a", "Bearer b"}, 401},
		{`{}`, []string{"Bearer token"}, 400},
		{`{"name":"  "}`, []string{"Bearer token"}, 400},
		{`{"name":"team","owner_id":42}`, []string{"Bearer token"}, 400},
		{`{"name":"team"}{"name":"second"}`, []string{"Bearer token"}, 400},
	} {
		r := httptest.NewRequest("POST", "/api/v1/teams", strings.NewReader(tc.body))
		for _, header := range tc.header {
			r.Header.Add("Authorization", header)
		}
		w := httptest.NewRecorder()
		createTeamHandler(client)(w, r)
		if w.Code != tc.want {
			t.Fatalf("body %q: got %d %s", tc.body, w.Code, w.Body.String())
		}
	}
}

func TestCreateTeamHTTPForwardsTokenAndReturnsID(t *testing.T) {
	client := teamClient{call: func(ctx context.Context, req *pb.CreateTeamRequest) (*pb.CreateTeamResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetName() != "Project A" || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
			t.Fatalf("wrong RPC request or token: %v, %v", req, md)
		}
		return &pb.CreateTeamResponse{TeamId: 9007199254740993}, nil
	}}
	r := httptest.NewRequest("POST", "/api/v1/teams", strings.NewReader(`{"name":" Project A "}`))
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	createTeamHandler(client)(w, r)
	var result struct {
		Code int `json:"code"`
		Data struct {
			TeamID string `json:"team_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Code != 0 || result.Data.TeamID != "9007199254740993" || strings.Contains(w.Body.String(), "test-token") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestCreateTeamHTTPMapsRPCErrors(t *testing.T) {
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
		client := teamClient{call: func(context.Context, *pb.CreateTeamRequest) (*pb.CreateTeamResponse, error) {
			return nil, status.Error(tc.code, "private database detail")
		}}
		r := httptest.NewRequest("POST", "/api/v1/teams", strings.NewReader(`{"name":"team"}`))
		r.Header.Set("Authorization", "Bearer test-token")
		w := httptest.NewRecorder()
		createTeamHandler(client)(w, r)
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private database detail") || strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("RPC %v: got %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
