package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/im/pb"
	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest/pathvar"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestGatewayConfigLoadsIMRPC(t *testing.T) {
	for _, tc := range []struct {
		file, endpoint string
	}{
		{"etc/api.yaml", "127.0.0.1:9002"},
		{"../deploy/api-gateway.yaml", "im-rpc:9002"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			var c config
			if err := conf.Load(tc.file, &c); err != nil {
				t.Fatal(err)
			}
			if len(c.IMRPC.Endpoints) != 1 || c.IMRPC.Endpoints[0] != tc.endpoint {
				t.Fatalf("IM RPC endpoints = %v, want %s", c.IMRPC.Endpoints, tc.endpoint)
			}
		})
	}
}

type teamGroupClient struct {
	pb.IMClient
	call func(context.Context, *pb.CreateTeamGroupRequest) (*pb.CreateTeamGroupResponse, error)
}

func (c teamGroupClient) CreateTeamGroup(ctx context.Context, req *pb.CreateTeamGroupRequest, _ ...grpc.CallOption) (*pb.CreateTeamGroupResponse, error) {
	return c.call(ctx, req)
}

func teamGroupRequest(teamID, body string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/teams/"+teamID+"/groups", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID})
	r.Header.Set("Idempotency-Key", "request-123")
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func TestCreateTeamGroupHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := teamGroupClient{call: func(context.Context, *pb.CreateTeamGroupRequest) (*pb.CreateTeamGroupResponse, error) {
		t.Fatal("invalid request reached IM RPC")
		return nil, nil
	}}
	for _, tc := range []struct {
		teamID, body string
		headers      []string
		want         int
	}{
		{"100", `{"name":"Planning"}`, nil, 401},
		{"100", `{"name":"Planning"}`, []string{"Bearer a", "Bearer b"}, 401},
		{"100", `{"name":"Planning"}`, []string{"Basic token"}, 401},
		{"0", `{"name":"Planning"}`, []string{"Bearer token"}, 400},
		{"bad", `{"name":"Planning"}`, []string{"Bearer token"}, 400},
		{"100", `{}`, []string{"Bearer token"}, 400},
		{"100", `{"name":" "}`, []string{"Bearer token"}, 400},
		{"100", `{"name":"Planning","owner_id":42}`, []string{"Bearer token"}, 400},
		{"100", `{"name":"Planning"}{"name":"Other"}`, []string{"Bearer token"}, 400},
	} {
		w := httptest.NewRecorder()
		createTeamGroupHandler(client)(w, teamGroupRequest(tc.teamID, tc.body, tc.headers...))
		if w.Code != tc.want {
			t.Fatalf("team=%q body=%q: got %d %s", tc.teamID, tc.body, w.Code, w.Body.String())
		}
	}
	for _, key := range []string{"", "space key", strings.Repeat("a", 65), "非ASCII"} {
		w := httptest.NewRecorder()
		r := teamGroupRequest("100", `{"name":"Planning"}`, "Bearer token")
		r.Header.Set("Idempotency-Key", key)
		createTeamGroupHandler(client)(w, r)
		if w.Code != 400 {
			t.Fatalf("key %q: got %d %s", key, w.Code, w.Body.String())
		}
	}
	for _, mutate := range []func(http.Header){
		func(h http.Header) { h.Del("Idempotency-Key") },
		func(h http.Header) { h.Add("Idempotency-Key", "another-request") },
	} {
		w := httptest.NewRecorder()
		r := teamGroupRequest("100", `{"name":"Planning"}`, "Bearer token")
		mutate(r.Header)
		createTeamGroupHandler(client)(w, r)
		if w.Code != 400 {
			t.Fatalf("Idempotency-Key headers %v: got %d %s", r.Header.Values("Idempotency-Key"), w.Code, w.Body.String())
		}
	}
}

func TestCreateTeamGroupHTTPForwardsTokenAndReturnsID(t *testing.T) {
	client := teamGroupClient{call: func(ctx context.Context, req *pb.CreateTeamGroupRequest) (*pb.CreateTeamGroupResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetName() != "Planning" || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" || len(md.Get("idempotency-key")) != 1 || md.Get("idempotency-key")[0] != "request-123" {
			t.Fatalf("wrong RPC request or token: %v, %v", req, md)
		}
		return &pb.CreateTeamGroupResponse{GroupId: 9007199254740995}, nil
	}}
	w := httptest.NewRecorder()
	createTeamGroupHandler(client)(w, teamGroupRequest("9007199254740993", `{"name":" Planning "}`, "Bearer test-token"))
	var result struct {
		Code int `json:"code"`
		Data struct {
			GroupID string `json:"group_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Code != 0 || result.Data.GroupID != "9007199254740995" || strings.Contains(w.Body.String(), "test-token") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestCreateTeamGroupHTTPMapsRPCErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.InvalidArgument, 400},
		{codes.Unauthenticated, 401},
		{codes.PermissionDenied, 403},
		{codes.AlreadyExists, 409},
		{codes.Unavailable, 503},
		{codes.DeadlineExceeded, 504},
		{codes.Internal, 502},
	} {
		client := teamGroupClient{call: func(context.Context, *pb.CreateTeamGroupRequest) (*pb.CreateTeamGroupResponse, error) {
			return nil, status.Error(tc.code, "private database detail")
		}}
		w := httptest.NewRecorder()
		createTeamGroupHandler(client)(w, teamGroupRequest("100", `{"name":"Planning"}`, "Bearer token"))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private database detail") || strings.Contains(w.Body.String(), `"data"`) {
			t.Fatalf("RPC %v: got %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
