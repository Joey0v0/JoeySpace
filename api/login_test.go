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
	"google.golang.org/grpc/status"
)

type loginClient struct {
	pb.UserClient
	call func(context.Context, *pb.LoginRequest) (*pb.LoginResponse, error)
}

func (c loginClient) Login(ctx context.Context, req *pb.LoginRequest, _ ...grpc.CallOption) (*pb.LoginResponse, error) {
	return c.call(ctx, req)
}

func TestLoginHTTPValidatesBeforeRPC(t *testing.T) {
	client := loginClient{call: func(context.Context, *pb.LoginRequest) (*pb.LoginResponse, error) {
		t.Fatal("invalid request reached RPC")
		return nil, nil
	}}
	for _, body := range []string{"", "{}", `{"username":"alice"}`, `{"password":"secret"}`, `{"username":"alice","password":"secret"} {}`, `{"username":"alice","password":"secret","extra":1}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/user/login", strings.NewReader(body))
		loginHandler(client)(w, r)
		if w.Code != 400 {
			t.Fatalf("body %q: got %d %s", body, w.Code, w.Body.String())
		}
	}
}

func TestLoginHTTPReturnsTokenWithoutChangingIt(t *testing.T) {
	client := loginClient{call: func(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
		if req.GetUsername() != "alice" || req.GetPassword() != "correct-password" {
			t.Fatalf("wrong RPC request: %v", req)
		}
		return &pb.LoginResponse{Token: "rpc-issued-token"}, nil
	}}
	w := httptest.NewRecorder()
	loginHandler(client)(w, httptest.NewRequest("POST", "/api/v1/user/login", strings.NewReader(`{"username":"alice","password":"correct-password"}`)))
	var body struct {
		Code int `json:"code"`
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || body.Code != 0 || body.Data.Token != "rpc-issued-token" {
		t.Fatalf("unexpected response: %d %s (%v)", w.Code, w.Body.String(), err)
	}
}

func TestLoginHTTPHidesRPCErrors(t *testing.T) {
	for _, tc := range []struct {
		rpcCode  codes.Code
		httpCode int
	}{
		{codes.Unauthenticated, 401}, {codes.PermissionDenied, 403}, {codes.Unavailable, 503}, {codes.DeadlineExceeded, 504},
	} {
		client := loginClient{call: func(context.Context, *pb.LoginRequest) (*pb.LoginResponse, error) {
			return nil, status.Error(tc.rpcCode, "private RPC detail")
		}}
		w := httptest.NewRecorder()
		loginHandler(client)(w, httptest.NewRequest("POST", "/api/v1/user/login", strings.NewReader(`{"username":"alice","password":"wrong"}`)))
		if w.Code != tc.httpCode || strings.Contains(w.Body.String(), "private RPC detail") || strings.Contains(w.Body.String(), "token") {
			t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
		}
	}
}
