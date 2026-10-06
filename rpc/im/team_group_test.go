package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/bwmarrin/snowflake"
	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type imPositiveID struct{}

func (imPositiveID) Match(value driver.Value) bool {
	id, ok := value.(int64)
	return ok && id > 0
}

type teamCreateFunc func(context.Context, *userpb.AuthorizeTeamGroupCreationRequest) error

func (f teamCreateFunc) AuthorizeTeamGroupCreation(ctx context.Context, req *userpb.AuthorizeTeamGroupCreationRequest, _ ...grpc.CallOption) (*userpb.AuthorizeTeamGroupCreationResponse, error) {
	if err := f(ctx, req); err != nil {
		return nil, err
	}
	return &userpb.AuthorizeTeamGroupCreationResponse{UserId: 42, Generation: 1}, nil
}

func (teamCreateFunc) CheckTeamMember(context.Context, *userpb.CheckTeamMemberRequest, ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by creation tests")
}

func teamGroupServer(t *testing.T) (*imServer, sqlmock.Sqlmock) {
	t.Helper()
	s, mock := testIMServer(t)
	node, err := snowflake.NewNode(3)
	if err != nil {
		t.Fatal(err)
	}
	s.idNode = node
	return s, mock
}

func expectTeamGroupInsert(mock sqlmock.Sqlmock, ownerErr error) {
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `groups`")).
		WithArgs("Planning", int64(42), int64(200), "request-123", imPositiveID{}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	owner := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `group_members`")).
		WithArgs(imPositiveID{}, int64(42), int8(2))
	if ownerErr != nil {
		owner.WillReturnError(ownerErr)
		mock.ExpectRollback()
	} else {
		owner.WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
	}
}

func TestCreateTeamGroupOverRPC(t *testing.T) {
	s, mock := teamGroupServer(t)
	token := "Bearer " + validIMToken(t)
	s.teamClient = teamCreateFunc(func(ctx context.Context, req *userpb.AuthorizeTeamGroupCreationRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != token {
			t.Errorf("wrong authorization request: team=%d, headers=%v", req.GetTeamId(), md.Get("authorization"))
		}
		return nil
	})
	expectTeamGroupInsert(mock, nil)
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", token, "idempotency-key", "request-123"))
	result, err := pb.NewIMClient(conn).CreateTeamGroup(ctx, &pb.CreateTeamGroupRequest{TeamId: 200, Name: " Planning "})
	if err != nil || result.GetGroupId() <= 0 {
		t.Fatalf("create team group RPC: %v, %v", result, err)
	}
}

func TestCreateTeamGroupRejectsInvalidInputAndAuthorization(t *testing.T) {
	s, _ := teamGroupServer(t)
	s.teamClient = teamCreateFunc(func(context.Context, *userpb.AuthorizeTeamGroupCreationRequest) error {
		return status.Error(codes.PermissionDenied, "team owner required")
	})
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t), "idempotency-key", "request-123"))
	for _, req := range []*pb.CreateTeamGroupRequest{nil, {TeamId: 0, Name: "Planning"}, {TeamId: 200, Name: " "}} {
		result, err := s.CreateTeamGroup(ctx, req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %v, %v", result, err)
		}
	}
	result, err := s.CreateTeamGroup(context.Background(), &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
	result, err = s.CreateTeamGroup(ctx, &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
	if result != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-owner: %v, %v", result, err)
	}
}

func TestCreateTeamGroupRollsBackWhenOwnerInsertFails(t *testing.T) {
	s, mock := teamGroupServer(t)
	s.teamClient = teamCreateFunc(func(context.Context, *userpb.AuthorizeTeamGroupCreationRequest) error { return nil })
	expectTeamGroupInsert(mock, errors.New("owner insert failed"))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t), "idempotency-key", "request-123"))
	result, err := s.CreateTeamGroup(ctx, &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
	if result != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("owner insert failure: %v, %v", result, err)
	}
}

func TestCreateTeamGroupDuplicateRequest(t *testing.T) {
	for _, tc := range []struct {
		name, previousName string
		previousTeam       int64
		want               codes.Code
	}{
		{"same request", "Planning", 200, codes.OK},
		{"different name", "Other", 200, codes.AlreadyExists},
		{"different team", "Planning", 201, codes.AlreadyExists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := teamGroupServer(t)
			s.teamClient = teamCreateFunc(func(context.Context, *userpb.AuthorizeTeamGroupCreationRequest) error { return nil })
			mock.ExpectBegin()
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `groups`")).
				WithArgs("Planning", int64(42), int64(200), "request-123", imPositiveID{}).
				WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate"})
			mock.ExpectRollback()
			mock.ExpectQuery(regexp.QuoteMeta("SELECT id, team_id, name FROM `groups` WHERE owner_id = ? AND request_key = ?")).
				WithArgs(int64(42), "request-123", 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "name"}).AddRow(int64(999), tc.previousTeam, tc.previousName))
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t), "idempotency-key", "request-123"))
			result, err := s.CreateTeamGroup(ctx, &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
			if status.Code(err) != tc.want || tc.want == codes.OK && result.GetGroupId() != 999 {
				t.Fatalf("result=%v error=%v, want %v", result, err, tc.want)
			}
		})
	}
}

func TestCreateTeamGroupRequiresRequestKey(t *testing.T) {
	s, _ := teamGroupServer(t)
	s.teamClient = teamCreateFunc(func(context.Context, *userpb.AuthorizeTeamGroupCreationRequest) error {
		t.Fatal("invalid key reached team authorization")
		return nil
	})
	for _, key := range []string{"", "has space", strings.Repeat("x", 65)} {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t), "idempotency-key", key))
		result, err := s.CreateTeamGroup(ctx, &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("key %q: %v, %v", key, result, err)
		}
	}
}
