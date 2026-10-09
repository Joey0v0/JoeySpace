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

func expectMyTaskTeamNames(mock sqlmock.Sqlmock, ids []int64) *sqlmock.ExpectedQuery {
	args := []driver.Value{int64(42), int64(0)}
	for _, id := range ids {
		args = append(args, id)
	}
	query := "SELECT teams.id AS team_id, teams.name FROM `team_members` JOIN teams ON teams.id = team_members.team_id WHERE team_members.user_id = ? AND team_members.membership_state = ? AND teams.id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ") ORDER BY teams.id ASC"
	return mock.ExpectQuery("^" + regexp.QuoteMeta(query) + "$").WithArgs(args...)
}

func TestMyTaskTeamNamesValidation(t *testing.T) {
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	for _, req := range []*pb.BatchGetMyTeamNamesRequest{nil, {}, {TeamIds: []int64{0}}, {TeamIds: []int64{-1}}, {TeamIds: []int64{1, 0}}, {TeamIds: ids}} {
		s, _ := newTestUserServer(t)
		got, err := s.BatchGetMyTeamNames(teamContext(t), req)
		if got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid %v: %v %v", req, got, err)
		}
	}
	s, _ := newTestUserServer(t)
	got, err := s.BatchGetMyTeamNames(context.Background(), &pb.BatchGetMyTeamNamesRequest{TeamIds: []int64{100}})
	if got != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing auth: %v %v", got, err)
	}
}

func TestMyTaskTeamNamesActiveScopeDedupAndPrecision(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	// Unknown and inactive caller membership rows are omitted by the exact SQL.
	expectMyTaskTeamNames(mock, []int64{100, 101, 102, 9007199254740993}).WillReturnRows(sqlmock.NewRows([]string{"team_id", "name"}).AddRow(100, "A").AddRow(int64(9007199254740993), "Large"))
	got, err := s.BatchGetMyTeamNames(teamContext(t), &pb.BatchGetMyTeamNamesRequest{TeamIds: []int64{100, 101, 100, 102, 9007199254740993}})
	if err != nil || len(got.GetTeams()) != 2 || got.Teams[0].Name != "A" || got.Teams[1].TeamId != 9007199254740993 {
		t.Fatalf("teams: %v %v", got, err)
	}
}

func TestMyTaskTeamNames100DistinctAfterDeduplication(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	ids := make([]int64, 100)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	expectMyTaskTeamNames(mock, ids).WillReturnRows(sqlmock.NewRows([]string{"team_id", "name"}))
	got, err := s.BatchGetMyTeamNames(teamContext(t), &pb.BatchGetMyTeamNamesRequest{TeamIds: append(ids, ids...)})
	if err != nil || got == nil || len(got.Teams) != 0 {
		t.Fatalf("100 teams: %v %v", got, err)
	}
}

func TestMyTaskTeamNamesDisabledCallerAndDBFailure(t *testing.T) {
	for _, name := range []string{"disabled caller", "DB failure"} {
		t.Run(name, func(t *testing.T) {
			s, mock := newTestUserServer(t)
			want := codes.Unavailable
			if name == "disabled caller" {
				want = codes.PermissionDenied
				mock.ExpectQuery("^"+regexp.QuoteMeta(profileQuery)+"$").WithArgs(int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 0))
			} else {
				expectTeamCreator(mock)
				expectMyTaskTeamNames(mock, []int64{100}).WillReturnError(errors.New("private DB detail"))
			}
			got, err := s.BatchGetMyTeamNames(teamContext(t), &pb.BatchGetMyTeamNamesRequest{TeamIds: []int64{100}})
			if got != nil || status.Code(err) != want || strings.Contains(err.Error(), "private DB detail") {
				t.Fatalf("failure: %v %v", got, err)
			}
		})
	}
}
