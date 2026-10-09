package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func expectTaskMemberNames(mock sqlmock.Sqlmock, ids []int64) *sqlmock.ExpectedQuery {
	args := []driver.Value{int64(100), int64(0)}
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, 1)
	query := "SELECT users.id, users.username, users.nickname FROM `team_members` JOIN users ON users.id = team_members.user_id WHERE team_members.team_id = ? AND team_members.membership_state = ? AND users.id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ") AND users.status = ? ORDER BY users.id ASC"
	return mock.ExpectQuery("^" + regexp.QuoteMeta(query) + "$").WithArgs(args...)
}

func TestTaskMemberNamesValidation(t *testing.T) {
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	for _, req := range []*pb.BatchGetTeamMemberDisplayNamesRequest{
		nil, {}, {TeamId: 0, UserIds: []int64{1}}, {TeamId: -1, UserIds: []int64{1}},
		{TeamId: 100}, {TeamId: 100, UserIds: []int64{0}}, {TeamId: 100, UserIds: []int64{-1}},
		{TeamId: 100, UserIds: []int64{1, 0}}, {TeamId: 100, UserIds: ids},
	} {
		s, _ := newTestUserServer(t)
		got, err := s.BatchGetTeamMemberDisplayNames(teamContext(t), req)
		if got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request %v: %v %v", req, got, err)
		}
	}
	s, _ := newTestUserServer(t)
	got, err := s.BatchGetTeamMemberDisplayNames(context.Background(), &pb.BatchGetTeamMemberDisplayNamesRequest{TeamId: 100, UserIds: []int64{1}})
	if got != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing auth: %v %v", got, err)
	}
}

func TestTaskMemberNamesActiveScopeFallbackAndPrecision(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectTeamMembership(mock, true, 0)
	// 8/9/10 represent unknown, inactive, and disabled targets; SQL must filter them.
	ids := []int64{5, 7, 8, 9, 10, 9007199254740993}
	expectTaskMemberNames(mock, ids).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname"}).AddRow(5, "bob", "Bob").AddRow(7, "carol", "").AddRow(int64(9007199254740993), "large", "Large"))
	got, err := s.BatchGetTeamMemberDisplayNames(teamContext(t), &pb.BatchGetTeamMemberDisplayNamesRequest{TeamId: 100, UserIds: append(ids, 5)})
	if err != nil || len(got.GetUsers()) != 3 || got.Users[0].DisplayName != "Bob" || got.Users[1].DisplayName != "carol" || got.Users[2].UserId != 9007199254740993 {
		t.Fatalf("names: %v %v", got, err)
	}
}

func TestTaskMemberNames100DistinctAfterDeduplication(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectTeamMembership(mock, true, 0)
	ids := make([]int64, 100)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	expectTaskMemberNames(mock, ids).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname"}))
	got, err := s.BatchGetTeamMemberDisplayNames(teamContext(t), &pb.BatchGetTeamMemberDisplayNamesRequest{TeamId: 100, UserIds: append(ids, ids...)})
	if err != nil || got == nil || len(got.Users) != 0 {
		t.Fatalf("100 distinct IDs: %v %v", got, err)
	}
}

func TestTaskMemberNamesCallerAndDBFailures(t *testing.T) {
	for _, name := range []string{"inactive caller", "disabled caller", "membership DB", "target DB"} {
		t.Run(name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			if name == "disabled caller" {
				mock.ExpectQuery("^"+regexp.QuoteMeta(profileQuery)+"$").WithArgs(int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 0))
			} else {
				expectTeamCreator(mock)
				if name == "membership DB" {
					mock.ExpectQuery("^"+regexp.QuoteMeta(activeTeamMembershipQuery)+"$").WithArgs(int64(100), int64(42), int64(0), 1).WillReturnError(errors.New("private DB detail"))
				} else {
					expectTeamMembership(mock, name != "inactive caller", 0)
				}
				if name == "target DB" {
					expectTaskMemberNames(mock, []int64{9}).WillReturnError(errors.New("private DB detail"))
				}
			}
			got, err := s.BatchGetTeamMemberDisplayNames(teamContext(t), &pb.BatchGetTeamMemberDisplayNamesRequest{TeamId: 100, UserIds: []int64{9}})
			want := codes.PermissionDenied
			if strings.Contains(name, "DB") {
				want = codes.Unavailable
			}
			if got != nil || status.Code(err) != want || strings.Contains(err.Error(), "private DB detail") {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
}
