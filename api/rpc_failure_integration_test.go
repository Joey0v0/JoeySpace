package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// 让三个真实业务入口通过同一条本机 TCP gRPC 连接，验证服务故障时的 HTTP 行为。
type slowUserRPC struct{ pb.UnimplementedUserServer }

func (slowUserRPC) Register(ctx context.Context, _ *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func (slowUserRPC) Login(ctx context.Context, _ *pb.LoginRequest) (*pb.LoginResponse, error) {
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func (slowUserRPC) GetMyInfo(ctx context.Context, _ *pb.GetMyInfoRequest) (*pb.GetUserInfoResponse, error) {
	<-ctx.Done()
	return nil, status.FromContextError(ctx.Err()).Err()
}

func startUserRPCForFailureTest(t *testing.T) (*grpc.Server, pb.UserClient) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterUserServer(server, slowUserRPC{})
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return server, pb.NewUserClient(conn)
}

func failedUserRequests(client pb.UserClient) []struct {
	name    string
	handler http.HandlerFunc
	request *http.Request
} {
	profile := httptest.NewRequest(http.MethodGet, "/api/v1/user/info", nil)
	profile.Header.Set("Authorization", "Bearer test-token")
	return []struct {
		name    string
		handler http.HandlerFunc
		request *http.Request
	}{
		{"register", registerHandler(client), httptest.NewRequest(http.MethodPost, "/api/v1/user/register", strings.NewReader(`{"username":"alice","password":"correct-password"}`))},
		{"login", loginHandler(client), httptest.NewRequest(http.MethodPost, "/api/v1/user/login", strings.NewReader(`{"username":"alice","password":"correct-password"}`))},
		{"profile", getMyInfoHandler(client), profile},
	}
}

func TestUserHTTPWhenRPCStops(t *testing.T) {
	server, client := startUserRPCForFailureTest(t)
	// 先建立连接，随后停止服务，检验已经连接的客户端遇到断链时的表现。
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = client.GetUserInfo(ctx, &pb.GetUserInfoRequest{UserId: 1})
	server.Stop()

	for _, tc := range failedUserRequests(client) {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(tc.request.Context(), time.Second)
			defer cancel()
			w := httptest.NewRecorder()
			tc.handler(w, tc.request.WithContext(ctx))
			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":10005`) {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestUserHTTPWhenRPCTimesOut(t *testing.T) {
	_, client := startUserRPCForFailureTest(t)
	for _, tc := range failedUserRequests(client) {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(tc.request.Context(), 50*time.Millisecond)
			defer cancel()
			w := httptest.NewRecorder()
			tc.handler(w, tc.request.WithContext(ctx))
			if w.Code != http.StatusGatewayTimeout || !strings.Contains(w.Body.String(), `"code":10005`) {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
