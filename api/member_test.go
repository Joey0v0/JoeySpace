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

type memberClient struct {
	pb.UserClient
	call func(context.Context, *pb.AddTeamMemberRequest) (*pb.AddTeamMemberResponse, error)
}

func (c memberClient) AddTeamMember(ctx context.Context, req *pb.AddTeamMemberRequest, _ ...grpc.CallOption) (*pb.AddTeamMemberResponse, error) {
	return c.call(ctx, req)
}

func memberRequest(teamID, body string, headers ...string) *http.Request {
	r := httptest.NewRequest("POST", "/api/v1/teams/"+teamID+"/members", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID})
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func TestAddTeamMemberHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := memberClient{call: func(context.Context, *pb.AddTeamMemberRequest) (*pb.AddTeamMemberResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	}}
	for _, tc := range []struct {
		teamID, body string
		headers      []string
		want         int
	}{
		{"100", `{"user_id":"7"}`, nil, 401},
		{"100", `{"user_id":"7"}`, []string{"Bearer a", "Bearer b"}, 401},
		{"0", `{"user_id":"7"}`, []string{"Bearer token"}, 400},
		{"bad", `{"user_id":"7"}`, []string{"Bearer token"}, 400},
		{"100", `{}`, []string{"Bearer token"}, 400},
		{"100", `{"user_id":7}`, []string{"Bearer token"}, 400},
		{"100", `{"user_id":"0"}`, []string{"Bearer token"}, 400},
		{"100", `{"user_id":"7","role":2}`, []string{"Bearer token"}, 400},
	} {
		w := httptest.NewRecorder()
		addTeamMemberHandler(client)(w, memberRequest(tc.teamID, tc.body, tc.headers...))
		if w.Code != tc.want {
			t.Fatalf("team %q, body %q: got %d %s", tc.teamID, tc.body, w.Code, w.Body.String())
		}
	}
}

func TestAddTeamMemberHTTPForwardsIDsAndToken(t *testing.T) {
	client := memberClient{call: func(ctx context.Context, req *pb.AddTeamMemberRequest) (*pb.AddTeamMemberResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetUserId() != 9007199254740995 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
			t.Fatalf("wrong RPC request: %v", req)
		}
		return &pb.AddTeamMemberResponse{}, nil
	}}
	w := httptest.NewRecorder()
	addTeamMemberHandler(client)(w, memberRequest("9007199254740993", `{"user_id":"9007199254740995"}`, "Bearer test-token"))
	if w.Code != 200 || strings.Contains(w.Body.String(), "test-token") || !strings.Contains(w.Body.String(), `"code":0`) {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}

func TestAddTeamMemberHTTPMapsRPCErrors(t *testing.T) {
	for _, tc := range []struct {
		code                 codes.Code
		wantStatus, wantCode int
	}{
		{codes.InvalidArgument, 400, 10001},
		{codes.Unauthenticated, 401, 10002},
		{codes.PermissionDenied, 403, 10003},
		{codes.NotFound, 404, 20002},
		{codes.FailedPrecondition, 409, 20004},
		{codes.AlreadyExists, 409, 60001},
		{codes.Unavailable, 503, 10005},
		{codes.DeadlineExceeded, 504, 10005},
		{codes.Internal, 502, 10005},
	} {
		client := memberClient{call: func(context.Context, *pb.AddTeamMemberRequest) (*pb.AddTeamMemberResponse, error) {
			return nil, status.Error(tc.code, "private database detail")
		}}
		w := httptest.NewRecorder()
		addTeamMemberHandler(client)(w, memberRequest("100", `{"user_id":"7"}`, "Bearer token"))
		var result response
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.wantStatus || result.Code != tc.wantCode || result.Data != nil || strings.Contains(w.Body.String(), "private database detail") {
			t.Fatalf("RPC %v: got %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
