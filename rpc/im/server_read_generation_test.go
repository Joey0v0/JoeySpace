package main

import (
	"context"
	"errors"
	"math"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type readMembershipResponseFunc struct {
	teamCheckFunc
	check func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error)
}

func (f readMembershipResponseFunc) CheckTeamMember(ctx context.Context, req *userpb.CheckTeamMemberRequest, _ ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	return f.check(ctx, req)
}

func expectReadMembershipSnapshot(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("^"+regexp.QuoteMeta(memberQuery)+"$").WithArgs(int64(100), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
}

func TestCheckGroupMemberRejectsInvalidUserResponseBeforeFence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *userpb.CheckTeamMemberResponse
	}{
		{"nil", nil},
		{"missing fields", &userpb.CheckTeamMemberResponse{}},
		{"different user", &userpb.CheckTeamMemberResponse{UserId: 77, Generation: 1}},
		{"missing user", &userpb.CheckTeamMemberResponse{Generation: 1}},
		{"missing generation", &userpb.CheckTeamMemberResponse{UserId: 42}},
		{"negative generation", &userpb.CheckTeamMemberResponse{UserId: 42, Generation: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			expectReadMembershipSnapshot(mock)
			calls := 0
			s.teamClient = readMembershipResponseFunc{check: func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
				calls++
				return tc.response, nil
			}}
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
			result, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
			// The exact response distinguishes early rejection from an unexpected SQL failure.
			if calls != 1 || result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() != "team membership authorization is invalid" {
				t.Fatalf("invalid User response: calls=%d result=%v error=%v", calls, result, err)
			}
		})
	}
}

func TestCheckGroupMemberRefreshesMemberAndClosureAfterUserAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name       string
		generation int64
		closed     any
		present    bool
		want       codes.Code
	}{
		{"active before fence initialization", 1, nil, true, codes.OK},
		{"initialized active", 1, int64(0), true, codes.OK},
		{"older generation with old member row", 1, int64(3), true, codes.PermissionDenied},
		{"closed generation with old member row", 3, int64(3), true, codes.PermissionDenied},
		{"member removed during User call", 4, int64(3), false, codes.PermissionDenied},
		{"group no longer belongs to initial team", 4, nil, false, codes.PermissionDenied},
		{"current explicitly joined member", 4, int64(3), true, codes.OK},
		{"large exact closed generation", 9007199254740993, int64(9007199254740993), true, codes.PermissionDenied},
		{"large exact current generation", 9007199254740994, int64(9007199254740993), true, codes.OK},
		{"maximum current generation", math.MaxInt64, int64(math.MaxInt64 - 1), true, codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			expectReadMembershipSnapshot(mock)
			expectTeamGroupReadFence(mock, 100, 42, 200, tc.closed, tc.present)
			calls := 0
			token := "Bearer " + validIMToken(t)
			s.teamClient = readMembershipResponseFunc{check: func(ctx context.Context, req *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
				calls++
				md, _ := metadata.FromOutgoingContext(ctx)
				if req.GetTeamId() != 200 || len(md) != 1 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != token {
					t.Error("User must receive the scoped team and only the original authorization")
				}
				return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: tc.generation}, nil
			}}
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", token, "user_id", "77"))
			ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("private-inherited", "must not forward"))
			result, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
			if calls != 1 || status.Code(err) != tc.want || (result != nil) != (tc.want == codes.OK) {
				t.Fatalf("fresh authorization: calls=%d result=%v error=%v", calls, result, err)
			}
		})
	}
}

func TestCheckGroupMemberFreshDatabaseFailuresNeverAuthorizeOldSnapshot(t *testing.T) {
	for _, kind := range []string{"database", "negative closure", "invalid closure type", "wrong group", "duplicate rows", "row error"} {
		t.Run(kind, func(t *testing.T) {
			s, mock := testIMServer(t)
			expectReadMembershipSnapshot(mock)
			s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
			expect := mock.ExpectQuery("^"+regexp.QuoteMeta(readFenceTestSQL)+"$").WithArgs(int64(100), int64(42), int64(200))
			rows := sqlmock.NewRows([]string{"group_id", "closed_through_generation"})
			switch kind {
			case "database":
				expect.WillReturnError(errors.New("private database detail"))
			case "negative closure":
				expect.WillReturnRows(rows.AddRow(100, -1))
			case "invalid closure type":
				expect.WillReturnRows(rows.AddRow(100, "private invalid value"))
			case "wrong group":
				expect.WillReturnRows(rows.AddRow(101, 0))
			case "duplicate rows":
				expect.WillReturnRows(rows.AddRow(100, 0).AddRow(100, 0))
			case "row error":
				expect.WillReturnRows(rows.AddRow(100, 0).RowError(0, errors.New("private row error")))
			}
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
			result, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
			if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() != "team group fence storage unavailable" {
				t.Fatalf("fresh database failure: result=%v error=%v", result, err)
			}
		})
	}
}

func TestCheckGroupMemberLegacyAndMissingGroupSkipUserAndFence(t *testing.T) {
	for _, present := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy member", false: "no member or nonexistent group"}[present], func(t *testing.T) {
			s, mock := testIMServer(t)
			s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error {
				t.Error("legacy or missing group must not call User")
				return status.Error(codes.Unavailable, "unexpected User call")
			})
			rows := sqlmock.NewRows([]string{"team_id"})
			if present {
				rows.AddRow(nil)
			}
			mock.ExpectQuery("^"+regexp.QuoteMeta(memberQuery)+"$").WithArgs(int64(100), int64(42), 1).WillReturnRows(rows)
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
			result, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
			want := codes.PermissionDenied
			if present {
				want = codes.OK
			}
			if status.Code(err) != want || (result != nil) != present {
				t.Fatalf("legacy/missing group: result=%v error=%v", result, err)
			}
		})
	}
}

func TestCheckGroupMemberCancellationAfterUserDoesNotRefresh(t *testing.T) {
	s, mock := testIMServer(t)
	expectReadMembershipSnapshot(mock)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.teamClient = readMembershipResponseFunc{check: func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		cancel()
		return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: 1}, nil
	}}
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
	result, err := s.CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
	if result != nil || status.Code(err) != codes.Canceled {
		t.Fatalf("cancellation after User: result=%v error=%v", result, err)
	}
}

func TestCheckGroupMemberCurrentAndClosedLargeGenerationOverRPC(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = readMembershipResponseFunc{check: func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: 9007199254740994}, nil
	}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterIMServer(server, s)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
	for _, tc := range []struct {
		closed  int64
		present bool
		want    codes.Code
	}{
		{9007199254740993, true, codes.OK},
		{9007199254740994, true, codes.PermissionDenied},
		{9007199254740993, false, codes.PermissionDenied},
	} {
		expectReadMembershipSnapshot(mock)
		expectTeamGroupReadFence(mock, 100, 42, 200, tc.closed, tc.present)
		result, err := pb.NewIMClient(conn).CheckGroupMember(ctx, &pb.CheckGroupMemberRequest{GroupId: 100})
		if status.Code(err) != tc.want || (result != nil) != (tc.want == codes.OK) {
			t.Fatalf("TCP team generation check: result=%v error=%v", result, err)
		}
	}
}
