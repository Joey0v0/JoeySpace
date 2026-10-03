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

type roleClient struct {
	pb.UserClient
	call func(context.Context, *pb.SetTeamMemberRoleRequest) (*pb.SetTeamMemberRoleResponse, error)
}

func (c roleClient) SetTeamMemberRole(ctx context.Context, req *pb.SetTeamMemberRoleRequest, _ ...grpc.CallOption) (*pb.SetTeamMemberRoleResponse, error) {
	return c.call(ctx, req)
}

func roleRequest(teamID, userID, body string, headers ...string) *http.Request {
	r := httptest.NewRequest("PUT", "/api/v1/teams/"+teamID+"/members/"+userID+"/role", strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"team_id": teamID, "user_id": userID})
	for _, header := range headers {
		r.Header.Add("Authorization", header)
	}
	return r
}

func TestRoleHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := roleClient{call: func(context.Context, *pb.SetTeamMemberRoleRequest) (*pb.SetTeamMemberRoleResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	}}
	for _, tc := range []struct {
		teamID, userID, body string
		headers              []string
		want                 int
	}{
		{"100", "7", `{"role":1}`, nil, 401},
		{"100", "7", `{"role":1}`, []string{"Bearer a", "Bearer b"}, 401},
		{"0", "7", `{"role":1}`, []string{"Bearer token"}, 400},
		{"100", "bad", `{"role":1}`, []string{"Bearer token"}, 400},
		{"100", "7", `{}`, []string{"Bearer token"}, 400},
		{"100", "7", `{"role":null}`, []string{"Bearer token"}, 400},
		{"100", "7", `{"role":2}`, []string{"Bearer token"}, 400},
		{"100", "7", `{"role":1,"owner_id":"42"}`, []string{"Bearer token"}, 400},
		{"100", "7", `{"role":1}{"role":0}`, []string{"Bearer token"}, 400},
	} {
		w := httptest.NewRecorder()
		setTeamMemberRoleHandler(client)(w, roleRequest(tc.teamID, tc.userID, tc.body, tc.headers...))
		if w.Code != tc.want {
			t.Fatalf("team %q, user %q, body %q: got %d %s", tc.teamID, tc.userID, tc.body, w.Code, w.Body.String())
		}
	}
}

func TestRoleHTTPForwardsIDsRoleAndToken(t *testing.T) {
	for _, role := range []int32{0, 1} {
		client := roleClient{call: func(ctx context.Context, req *pb.SetTeamMemberRoleRequest) (*pb.SetTeamMemberRoleResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if req.GetTeamId() != 9007199254740993 || req.GetUserId() != 9007199254740995 || req.GetRole() != role || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
				t.Fatalf("wrong RPC request: %v", req)
			}
			return &pb.SetTeamMemberRoleResponse{}, nil
		}}
		w := httptest.NewRecorder()
		body := `{"role":1}`
		if role == 0 {
			body = `{"role":0}`
		}
		setTeamMemberRoleHandler(client)(w, roleRequest("9007199254740993", "9007199254740995", body, "Bearer test-token"))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":0`) || strings.Contains(w.Body.String(), "test-token") {
			t.Fatalf("role %d: %d %s", role, w.Code, w.Body.String())
		}
	}
}

func TestRoleHTTPMapsRPCErrors(t *testing.T) {
	for _, tc := range []struct {
		code                 codes.Code
		wantStatus, wantCode int
	}{
		{codes.InvalidArgument, 400, 10001},
		{codes.Unauthenticated, 401, 10002},
		{codes.PermissionDenied, 403, 10003},
		{codes.NotFound, 404, 10004},
		{codes.FailedPrecondition, 409, 60002},
		{codes.Unavailable, 503, 10005},
		{codes.DeadlineExceeded, 504, 10005},
		{codes.Internal, 502, 10005},
	} {
		client := roleClient{call: func(context.Context, *pb.SetTeamMemberRoleRequest) (*pb.SetTeamMemberRoleResponse, error) {
			return nil, status.Error(tc.code, "private database detail")
		}}
		w := httptest.NewRecorder()
		setTeamMemberRoleHandler(client)(w, roleRequest("100", "7", `{"role":1}`, "Bearer token"))
		var result response
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.wantStatus || result.Code != tc.wantCode || result.Data != nil || strings.Contains(w.Body.String(), "private database detail") {
			t.Fatalf("RPC %v: got %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
