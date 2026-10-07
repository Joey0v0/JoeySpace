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

type leaveHTTPClient struct {
	pb.UserClient
	leave func(context.Context, *pb.LeaveTeamRequest) (*pb.TeamLeaveOperationResponse, error)
	get   func(context.Context, *pb.GetTeamLeaveOperationRequest) (*pb.TeamLeaveOperationResponse, error)
}

func (c leaveHTTPClient) LeaveTeam(ctx context.Context, req *pb.LeaveTeamRequest, _ ...grpc.CallOption) (*pb.TeamLeaveOperationResponse, error) {
	return c.leave(ctx, req)
}

func (c leaveHTTPClient) GetTeamLeaveOperation(ctx context.Context, req *pb.GetTeamLeaveOperationRequest, _ ...grpc.CallOption) (*pb.TeamLeaveOperationResponse, error) {
	return c.get(ctx, req)
}

func leaveHTTPRequest(method, team, body, query string, token bool) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/teams/"+team+"/leave"+query, strings.NewReader(body))
	r = pathvar.WithVars(r, map[string]string{"team_id": team})
	if token {
		r.Header.Set("Authorization", "Bearer test-token")
	}
	return r
}

func TestLeaveTeamHTTPForwardsTokenAndExactKey(t *testing.T) {
	client := leaveHTTPClient{leave: func(ctx context.Context, req *pb.LeaveTeamRequest) (*pb.TeamLeaveOperationResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 9007199254740993 || req.GetRequestKey() != "leave-key" || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer test-token" {
			t.Fatalf("unexpected RPC scope %v, %v", req, md)
		}
		return &pb.TeamLeaveOperationResponse{OperationId: 9007199254740995, TeamId: req.GetTeamId(), Generation: 9007199254740997, Status: 1}, nil
	}}
	w := httptest.NewRecorder()
	leaveTeamHandler(client)(w, leaveHTTPRequest(http.MethodPost, "9007199254740993", `{"request_key":"leave-key"}`, "", true))
	var got teamLeaveHTTPResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || got.Data == nil || got.Data.OperationID != "9007199254740995" || got.Data.Generation != "9007199254740997" || got.Data.Status != 1 || strings.Contains(w.Body.String(), "test-token") {
		t.Fatalf("unexpected response %d %s", w.Code, w.Body.String())
	}
}

func TestTeamLeaveHTTPQueryPendingAfterExit(t *testing.T) {
	client := leaveHTTPClient{get: func(ctx context.Context, req *pb.GetTeamLeaveOperationRequest) (*pb.TeamLeaveOperationResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 100 || req.GetRequestKey() != "same-key" || md.Get("authorization")[0] != "Bearer test-token" {
			t.Fatalf("unexpected query %v, %v", req, md)
		}
		return &pb.TeamLeaveOperationResponse{OperationId: 89, TeamId: 100, Generation: 7, Status: 0}, nil
	}}
	w := httptest.NewRecorder()
	getTeamLeaveOperationHandler(client)(w, leaveHTTPRequest(http.MethodGet, "100", "", "?request_key=same-key", true))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":0`) || !strings.Contains(w.Body.String(), `"operation_id":"89"`) {
		t.Fatalf("unexpected query response %d %s", w.Code, w.Body.String())
	}
}

func TestTeamLeaveHTTPRejectsInvalidRequestBeforeRPC(t *testing.T) {
	client := leaveHTTPClient{
		leave: func(context.Context, *pb.LeaveTeamRequest) (*pb.TeamLeaveOperationResponse, error) {
			t.Fatal("invalid leave reached RPC")
			return nil, nil
		},
		get: func(context.Context, *pb.GetTeamLeaveOperationRequest) (*pb.TeamLeaveOperationResponse, error) {
			t.Fatal("invalid query reached RPC")
			return nil, nil
		},
	}
	for _, tc := range []struct {
		method, team, body, query string
		token, want               bool
	}{
		{http.MethodPost, "100", `{"request_key":"a"}`, "", false, false},
		{http.MethodPost, "0", `{"request_key":"a"}`, "", true, false},
		{http.MethodPost, "100", `{"request_key":"bad key"}`, "", true, false},
		{http.MethodPost, "100", `{"request_key":"a","user_id":"42"}`, "", true, false},
		{http.MethodPost, "100", `{"request_key":"a"}{}`, "", true, false},
		{http.MethodGet, "100", "", "?request_key=a&user_id=42", true, false},
		{http.MethodGet, "100", "", "?request_key=a&request_key=b", true, false},
		{http.MethodGet, "100", "", "?request_key=bad%20key", true, false},
	} {
		w := httptest.NewRecorder()
		r := leaveHTTPRequest(tc.method, tc.team, tc.body, tc.query, tc.token)
		if tc.method == http.MethodPost {
			leaveTeamHandler(client)(w, r)
		} else {
			getTeamLeaveOperationHandler(client)(w, r)
		}
		if w.Code != 400 && w.Code != 401 {
			t.Fatalf("%s %s: %d %s", tc.method, tc.query, w.Code, w.Body.String())
		}
	}
}

func TestTeamLeaveHTTPDoesNotTreatPendingLeaveAsSuccess(t *testing.T) {
	client := leaveHTTPClient{leave: func(context.Context, *pb.LeaveTeamRequest) (*pb.TeamLeaveOperationResponse, error) {
		return &pb.TeamLeaveOperationResponse{OperationId: 89, TeamId: 100, Generation: 7, Status: 0}, nil
	}}
	w := httptest.NewRecorder()
	leaveTeamHandler(client)(w, leaveHTTPRequest(http.MethodPost, "100", `{"request_key":"same-key"}`, "", true))
	if w.Code != 502 || strings.Contains(w.Body.String(), `"data"`) {
		t.Fatalf("pending leave should not be reported complete: %d %s", w.Code, w.Body.String())
	}
}

func TestTeamLeaveHTTPMapsPrivateErrors(t *testing.T) {
	for _, tc := range []struct {
		code codes.Code
		want int
	}{
		{codes.AlreadyExists, 409}, {codes.FailedPrecondition, 409}, {codes.NotFound, 404},
		{codes.Unavailable, 503}, {codes.DeadlineExceeded, 504},
	} {
		client := leaveHTTPClient{get: func(context.Context, *pb.GetTeamLeaveOperationRequest) (*pb.TeamLeaveOperationResponse, error) {
			return nil, status.Error(tc.code, "private database detail")
		}}
		w := httptest.NewRecorder()
		getTeamLeaveOperationHandler(client)(w, leaveHTTPRequest(http.MethodGet, "100", "", "?request_key=same-key", true))
		if w.Code != tc.want || strings.Contains(w.Body.String(), "private database detail") {
			t.Fatalf("%v: %d %s", tc.code, w.Code, w.Body.String())
		}
	}
}
