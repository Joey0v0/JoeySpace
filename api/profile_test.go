package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type profileClient struct {
	pb.UserClient
	call func(context.Context) (*pb.GetUserInfoResponse, error)
}

func (c profileClient) GetMyInfo(ctx context.Context, _ *pb.GetMyInfoRequest, _ ...grpc.CallOption) (*pb.GetUserInfoResponse, error) {
	return c.call(ctx)
}

func TestProfileHTTPRejectsIDAndMissingCredentials(t *testing.T) {
	client := profileClient{call: func(context.Context) (*pb.GetUserInfoResponse, error) { t.Fatal("must not call RPC"); return nil, nil }}
	for _, tc := range []struct {
		query, header string
		code          int
	}{
		{"?user_id=99", "Bearer token", 400}, {"", "", 401}, {"", "Bearer", 401}, {"", "Basic token", 401},
	} {
		r := httptest.NewRequest("GET", "/api/v1/user/info"+tc.query, nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		w := httptest.NewRecorder()
		getMyInfoHandler(client)(w, r)
		if w.Code != tc.code {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	}
}

func TestProfileHTTPForwardsCredentialAndResult(t *testing.T) {
	client := profileClient{call: func(ctx context.Context) (*pb.GetUserInfoResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer test-token" {
			t.Fatal("credential not forwarded")
		}
		return &pb.GetUserInfoResponse{Id: 42, Username: "alice", Nickname: "Alice"}, nil
	}}
	r := httptest.NewRequest("GET", "/api/v1/user/info", nil)
	r.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()
	getMyInfoHandler(client)(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"42"`) || strings.Contains(w.Body.String(), "test-token") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestProfileHTTPInvalidToken(t *testing.T) {
	client := profileClient{call: func(context.Context) (*pb.GetUserInfoResponse, error) {
		return nil, status.Error(codes.Unauthenticated, "invalid token")
	}}
	r := httptest.NewRequest("GET", "/api/v1/user/info", nil)
	r.Header.Set("Authorization", "Bearer fake")
	w := httptest.NewRecorder()
	getMyInfoHandler(client)(w, r)
	if w.Code != 401 || strings.Contains(w.Body.String(), `"data"`) {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
