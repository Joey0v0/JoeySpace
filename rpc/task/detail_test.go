package main

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const detailSQL = "SELECT id, team_id, title, description, creator_id, assignee_id, status, source_group_id, source_message_id, due_at_unix_ms FROM `tasks` WHERE team_id = ? AND id = ? LIMIT ?"

var personalColumns = []string{"id", "team_id", "title", "description", "creator_id", "assignee_id", "status", "source_group_id", "source_message_id", "due_at_unix_ms"}

func TestGetTaskValidationAndMembership(t *testing.T) {
	s, _ := testTaskServer(t)
	for _, req := range []*pb.GetTaskRequest{nil, {}, {TeamId: -1, TaskId: 2}, {TeamId: 200}, {TeamId: 200, TaskId: -1}} {
		if r, err := s.GetTask(taskListContext(), req); r != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid detail: %v %v", r, err)
		}
	}
	if _, err := s.GetTask(context.Background(), &pb.GetTaskRequest{TeamId: 200, TaskId: 1}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	s.teamClient = teamCheckerFake{checkMember: func(ctx context.Context, id int64) (*userpb.CheckTeamMemberResponse, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		if id != 200 || md.Get("authorization")[0] != "Bearer sample" {
			t.Fatal("membership metadata lost")
		}
		return nil, status.Error(codes.PermissionDenied, "departed")
	}}
	if r, err := s.GetTask(taskListContext(), &pb.GetTaskRequest{TeamId: 200, TaskId: 1}); r != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("departure: %v %v", r, err)
	}
}

func TestGetTaskPermissionAndNullableMapping(t *testing.T) {
	for _, tc := range []struct {
		name            string
		caller, creator int64
		assignee        any
		role            int32
		want            bool
	}{
		{"creator", 42, 42, nil, 0, true}, {"assignee", 42, 7, int64(42), 0, true}, {"owner", 42, 7, nil, 2, true}, {"admin", 42, 7, nil, 1, false}, {"member", 42, 7, int64(9), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testTaskServer(t)
			s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
				return &userpb.CheckTeamMemberResponse{UserId: tc.caller, Role: tc.role}, nil
			}}
			const id int64 = 9007199254740993
			mock.ExpectQuery(regexp.QuoteMeta(detailSQL)).WithArgs(int64(200), id, 1).WillReturnRows(sqlmock.NewRows(personalColumns).AddRow(id, 200, "Task", "Details", tc.creator, tc.assignee, 1, nil, nil, nil))
			r, err := s.GetTask(taskListContext(), &pb.GetTaskRequest{TeamId: 200, TaskId: id})
			if err != nil || r.GetCanUpdateStatus() != tc.want || r.GetTask().GetTaskId() != id || r.GetTask().GetTeamId() != 200 || r.GetTask().GetDueAtUnixMs() != 0 || r.GetTask().GetSourceGroupId() != 0 {
				t.Fatalf("detail: %v %v", r, err)
			}
		})
	}
}

func TestGetTaskMissingAndDatabaseFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dbErr error
		want  codes.Code
	}{{"missing", nil, codes.NotFound}, {"database", errors.New("private database error"), codes.Unavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testTaskServer(t)
			q := mock.ExpectQuery(regexp.QuoteMeta(detailSQL)).WithArgs(int64(200), int64(1), 1)
			if tc.dbErr == nil {
				q.WillReturnRows(sqlmock.NewRows(personalColumns))
			} else {
				q.WillReturnError(tc.dbErr)
			}
			r, err := s.GetTask(taskListContext(), &pb.GetTaskRequest{TeamId: 200, TaskId: 1})
			if r != nil || status.Code(err) != tc.want {
				t.Fatalf("detail error: %v %v", r, err)
			}
		})
	}
}
