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

func TestConversationDisplayNamesSafeFieldsAndDeduplication(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	// IDs 8 and 9 represent unknown and disabled users; neither is selected.
	mock.ExpectQuery("^"+regexp.QuoteMeta("SELECT id, username, nickname FROM `users` WHERE id IN (?,?,?,?,?) AND status = ? ORDER BY id ASC")+"$").WithArgs(int64(5), int64(7), int64(8), int64(9), int64(9007199254740993), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname"}).AddRow(5, "bob", "Bob").AddRow(7, "carol", "").AddRow(9007199254740993, "large", "Large"))
	got, err := s.BatchGetConversationDisplayNames(teamContext(t), &pb.BatchGetConversationDisplayNamesRequest{UserIds: []int64{5, 7, 5, 8, 9, 9007199254740993}})
	if err != nil || len(got.GetUsers()) != 3 || got.GetUsers()[0].GetDisplayName() != "Bob" || got.GetUsers()[1].GetDisplayName() != "carol" || got.GetUsers()[2].GetUserId() != 9007199254740993 {
		t.Fatalf("names: %v, %v", got, err)
	}
}

func TestConversationDisplayNamesRejectsInvalidIDsAndMissingIdentity(t *testing.T) {
	s, _ := newTestUserServer(t)
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	for _, values := range [][]int64{nil, {0}, {-1}, {5, 0}, ids} {
		got, err := s.BatchGetConversationDisplayNames(teamContext(t), &pb.BatchGetConversationDisplayNamesRequest{UserIds: values})
		if got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid IDs: %v, %v", got, err)
		}
	}
	got, err := s.BatchGetConversationDisplayNames(context.Background(), &pb.BatchGetConversationDisplayNamesRequest{UserIds: []int64{5}})
	if got != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing identity: %v, %v", got, err)
	}
}

func TestConversationDisplayNamesDisabledCaller(t *testing.T) {
	s, mock := newTestUserServer(t)
	mock.ExpectQuery(regexp.QuoteMeta(profileQuery)).WithArgs(int64(42), 1).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname", "status"}).AddRow(42, "alice", "Alice", 0))
	got, err := s.BatchGetConversationDisplayNames(teamContext(t), &pb.BatchGetConversationDisplayNamesRequest{UserIds: []int64{5}})
	if got != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("disabled caller: %v, %v", got, err)
	}
}

func TestConversationDisplayNamesUnavailableIsNotEmpty(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown or disabled", true: "failure"}[fail], func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectTeamCreator(mock)
			q := mock.ExpectQuery("^"+regexp.QuoteMeta("SELECT id, username, nickname FROM `users` WHERE id IN (?) AND status = ? ORDER BY id ASC")+"$").WithArgs(int64(9), 1)
			if fail {
				q.WillReturnError(errors.New("database lost"))
			} else {
				q.WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname"}))
			}
			got, err := s.BatchGetConversationDisplayNames(teamContext(t), &pb.BatchGetConversationDisplayNamesRequest{UserIds: []int64{9}})
			if fail {
				if got != nil || status.Code(err) != codes.Unavailable {
					t.Fatalf("failure: %v, %v", got, err)
				}
			} else if err != nil || got == nil || len(got.GetUsers()) != 0 {
				t.Fatalf("empty: %v, %v", got, err)
			}
		})
	}
}

func TestConversationDisplayNamesAccepts100IDs(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	ids := make([]int64, 100)
	args := make([]driver.Value, 0, 101)
	for i := range ids {
		ids[i] = int64(i + 1)
		args = append(args, ids[i])
	}
	args = append(args, 1)
	query := "SELECT id, username, nickname FROM `users` WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", 100), ",") + ") AND status = ? ORDER BY id ASC"
	mock.ExpectQuery("^" + regexp.QuoteMeta(query) + "$").WithArgs(args...).WillReturnRows(sqlmock.NewRows([]string{"id", "username", "nickname"}).AddRow(100, "last", "Last"))
	got, err := s.BatchGetConversationDisplayNames(teamContext(t), &pb.BatchGetConversationDisplayNamesRequest{UserIds: ids})
	if err != nil || len(got.GetUsers()) != 1 || got.GetUsers()[0].GetUserId() != 100 {
		t.Fatalf("100 IDs: %v, %v", got, err)
	}
}
