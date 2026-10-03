package main

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func teamGroupListContext(t *testing.T) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
}

func TestListTeamGroupsPage(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || len(md.Get("authorization")) != 1 {
			t.Fatalf("wrong team authorization: %v, %v", req, md)
		}
		return nil
	})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name, owner_id FROM `groups` WHERE team_id = ? AND id > ? ORDER BY id ASC LIMIT ?")).
		WithArgs(int64(200), int64(10), 3).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "owner_id"}).
			AddRow(int64(11), "Planning", int64(42)).
			AddRow(int64(12), "Delivery", int64(43)).
			AddRow(int64(13), "Extra", int64(44)))
	result, err := s.ListTeamGroups(teamGroupListContext(t), &pb.ListTeamGroupsRequest{TeamId: 200, AfterGroupId: 10, Limit: 2})
	if err != nil || len(result.GetGroups()) != 2 || result.GetGroups()[0].GetGroupId() != 11 || result.GetGroups()[1].GetName() != "Delivery" || result.GetNextAfterGroupId() != 12 {
		t.Fatalf("list result: %v, %v", result, err)
	}
}

func TestListTeamGroupsRejectsNonMemberBeforeDatabaseQuery(t *testing.T) {
	s, _ := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error {
		return status.Error(codes.PermissionDenied, "not a team member")
	})
	result, err := s.ListTeamGroups(teamGroupListContext(t), &pb.ListTeamGroupsRequest{TeamId: 200})
	if result != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-member list: %v, %v", result, err)
	}
}

func TestListTeamGroupsRejectsInvalidRequestAndDatabaseFailure(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	for _, req := range []*pb.ListTeamGroupsRequest{nil, {TeamId: 0}, {TeamId: 200, AfterGroupId: -1}, {TeamId: 200, Limit: 101}} {
		result, err := s.ListTeamGroups(teamGroupListContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %v, %v", result, err)
		}
	}
	result, err := s.ListTeamGroups(context.Background(), &pb.ListTeamGroupsRequest{TeamId: 200})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, name, owner_id FROM `groups` WHERE team_id = ? AND id > ? ORDER BY id ASC LIMIT ?")).
		WithArgs(int64(200), int64(0), 21).WillReturnError(errors.New("private DB detail"))
	result, err = s.ListTeamGroups(teamGroupListContext(t), &pb.ListTeamGroupsRequest{TeamId: 200})
	if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private DB detail" {
		t.Fatalf("database failure: %v, %v", result, err)
	}
}
