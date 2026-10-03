package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type registerClient struct {
	pb.UserClient
	call func(context.Context, *pb.RegisterRequest) (*pb.RegisterResponse, error)
}

func (c registerClient) Register(ctx context.Context, req *pb.RegisterRequest, _ ...grpc.CallOption) (*pb.RegisterResponse, error) {
	return c.call(ctx, req)
}

func TestRegisterHTTPValidatesBeforeRPC(t *testing.T) {
	client := registerClient{call: func(context.Context, *pb.RegisterRequest) (*pb.RegisterResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	}}
	for _, body := range []string{
		`{}`, `{"username":"ab","password":"password"}`, `{"username":"alice","password":"short"}`,
		`{"username":"alice","password":"password","unknown":1}`,
	} {
		w := httptest.NewRecorder()
		registerHandler(client)(w, httptest.NewRequest("POST", "/api/v1/user/register", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("body %q: got %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestRegisterHTTPForwardsDataAndMapsErrors(t *testing.T) {
	client := registerClient{call: func(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
		if req.GetUsername() != "alice" || req.GetPassword() != "correct-password" || req.GetNickname() != "Alice" {
			t.Fatalf("wrong RPC request: %v", req)
		}
		return &pb.RegisterResponse{}, nil
	}}
	w := httptest.NewRecorder()
	registerHandler(client)(w, httptest.NewRequest("POST", "/api/v1/user/register", strings.NewReader(`{"username":"alice","password":"correct-password","nickname":"Alice"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":0`) || strings.Contains(w.Body.String(), "correct-password") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}

	for _, tc := range []struct {
		code codes.Code
		http int
	}{
		{codes.AlreadyExists, 409}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504},
	} {
		failed := registerClient{call: func(context.Context, *pb.RegisterRequest) (*pb.RegisterResponse, error) {
			return nil, status.Error(tc.code, "private database detail")
		}}
		w := httptest.NewRecorder()
		registerHandler(failed)(w, httptest.NewRequest("POST", "/api/v1/user/register", strings.NewReader(`{"username":"alice","password":"correct-password"}`)))
		if w.Code != tc.http || strings.Contains(w.Body.String(), "private database detail") {
			t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
		}
	}
}
