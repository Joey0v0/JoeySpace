package main

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

type taskIdentityFake func(context.Context) (*userpb.GetUserInfoResponse, error)

func (f taskIdentityFake) GetMyInfo(ctx context.Context, _ *userpb.GetMyInfoRequest, _ ...grpc.CallOption) (*userpb.GetUserInfoResponse, error) {
	return f(ctx)
}

type taskDirectoryFake func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error)

func (f taskDirectoryFake) ListMyTeams(ctx context.Context, req *userpb.ListMyTeamsRequest, _ ...grpc.CallOption) (*userpb.ListMyTeamsResponse, error) {
	return f(ctx, req)
}

const personalNow int64 = 1791500000000
const personalUpper int64 = 9007199254740999

func testPersonalServer(t *testing.T) (*taskServer, sqlmock.Sqlmock) {
	t.Helper()
	s, mock := testTaskServer(t)
	s.now = func() time.Time { return time.UnixMilli(personalNow) }
	s.identityClient = taskIdentityFake(func(ctx context.Context) (*userpb.GetUserInfoResponse, error) {
		requirePersonalBearer(t, ctx)
		return &userpb.GetUserInfoResponse{Id: 42}, nil
	})
	s.teamDirectory = taskDirectoryFake(func(ctx context.Context, req *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		requirePersonalBearer(t, ctx)
		if req.GetLimit() != 100 || req.GetAfterTeamId() != 0 {
			t.Fatalf("unexpected directory request: %v", req)
		}
		return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 200}, {TeamId: 300}}}, nil
	})
	return s, mock
}
func requirePersonalBearer(t *testing.T, ctx context.Context) {
	t.Helper()
	md, _ := metadata.FromOutgoingContext(ctx)
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer sample" {
		t.Fatalf("Bearer lost: %v", md)
	}
}
