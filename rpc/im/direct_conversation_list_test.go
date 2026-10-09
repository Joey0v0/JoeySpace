package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"regexp"
	"testing"
)

const testDirectSnapshotSQL = "SELECT COALESCE(MAX(id), 0) AS upper_id FROM messages WHERE chat_type = 1 AND ((from_id = ? AND to_id > 0 AND to_id <> ?) OR (to_id = ? AND from_id > 0 AND from_id <> ?))"
const testDirectDirectorySQL = `SELECT peer_id, MAX(id) AS last_message_id FROM (
SELECT to_id AS peer_id, id FROM messages WHERE chat_type = 1 AND from_id = ? AND to_id > 0 AND to_id <> ? AND id <= ?
UNION ALL
SELECT from_id AS peer_id, id FROM messages WHERE chat_type = 1 AND to_id = ? AND from_id > 0 AND from_id <> ? AND id <= ?
) AS direct_messages GROUP BY peer_id HAVING (? = 0 OR MAX(id) < ?) ORDER BY last_message_id DESC LIMIT ?`

func TestDirectDirectorySnapshotPages(t *testing.T) {
	s, m := testIMServer(t)
	const upper int64 = 9007199254740999
	m.ExpectQuery(regexp.QuoteMeta(testDirectSnapshotSQL)).WithArgs(int64(42), int64(42), int64(42), int64(42)).WillReturnRows(sqlmock.NewRows([]string{"upper_id"}).AddRow(upper))
	m.ExpectQuery(regexp.QuoteMeta(testDirectDirectorySQL)).WithArgs(int64(42), int64(42), upper, int64(42), int64(42), upper, int64(0), int64(0), 2).WillReturnRows(sqlmock.NewRows([]string{"peer_id", "last_message_id"}).AddRow(9007199254740993, upper).AddRow(43, upper-1))
	got, err := s.ListMyDirectConversations(teamGroupListContext(t), &pb.ListMyDirectConversationsRequest{Limit: 1})
	if err != nil || len(got.GetConversations()) != 1 || got.GetConversations()[0].GetPeerId() != 9007199254740993 || got.GetNextBeforeLastMessageId() != upper || got.GetSnapshotUpperMessageId() != upper {
		t.Fatalf("%v %v", got, err)
	}
	// The second query retains the first snapshot: new messages cannot move an old peer past the cursor.
	m.ExpectQuery(regexp.QuoteMeta(testDirectDirectorySQL)).WithArgs(int64(42), int64(42), upper, int64(42), int64(42), upper, upper, upper, 2).WillReturnRows(sqlmock.NewRows([]string{"peer_id", "last_message_id"}).AddRow(43, upper-1))
	next, err := s.ListMyDirectConversations(teamGroupListContext(t), &pb.ListMyDirectConversationsRequest{SnapshotUpperMessageId: upper, BeforeLastMessageId: upper, Limit: 1})
	if err != nil || len(next.GetConversations()) != 1 || next.GetConversations()[0].GetPeerId() != 43 || next.GetNextBeforeLastMessageId() != 0 {
		t.Fatalf("%v %v", next, err)
	}
}
func TestDirectDirectoryEmptyAndUnavailable(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "no persistent messages", true: "database failure"}[fail], func(t *testing.T) {
			s, m := testIMServer(t)
			q := m.ExpectQuery(regexp.QuoteMeta(testDirectSnapshotSQL)).WithArgs(int64(42), int64(42), int64(42), int64(42))
			if fail {
				q.WillReturnError(errors.New("private"))
			} else {
				q.WillReturnRows(sqlmock.NewRows([]string{"upper_id"}).AddRow(0))
			}
			got, err := s.ListMyDirectConversations(teamGroupListContext(t), &pb.ListMyDirectConversationsRequest{})
			if fail {
				if got != nil || status.Code(err) != codes.Unavailable {
					t.Fatalf("%v %v", got, err)
				}
			} else if err != nil || len(got.GetConversations()) != 0 || got.GetSnapshotUpperMessageId() != 0 {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
}
func TestDirectDirectoryValidation(t *testing.T) {
	s, _ := testIMServer(t)
	for _, r := range []*pb.ListMyDirectConversationsRequest{{SnapshotUpperMessageId: -1}, {BeforeLastMessageId: -1}, {BeforeLastMessageId: 2}, {SnapshotUpperMessageId: 2, BeforeLastMessageId: 3}, {Limit: 101}, {Limit: -1}} {
		got, err := s.ListMyDirectConversations(teamGroupListContext(t), r)
		if got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%v %v", got, err)
		}
	}
	got, err := s.ListMyDirectConversations(context.Background(), &pb.ListMyDirectConversationsRequest{})
	if got != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("%v %v", got, err)
	}
}

func TestDirectDirectoryUsesTokenIdentityAndNoTeamMembership(t *testing.T) {
	for _, userID := range []int64{42, 43} {
		t.Run(fmt.Sprint(userID), func(t *testing.T) {
			s, m := testIMServer(t) // No team client: cross-team persistent history remains discoverable.
			token, err := pkgjwt.GenerateToken(userID, imTestSecret, 1)
			if err != nil {
				t.Fatal(err)
			}
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
			m.ExpectQuery(regexp.QuoteMeta(testDirectDirectorySQL)).WithArgs(userID, userID, int64(100), userID, userID, int64(100), int64(0), int64(0), 21).WillReturnRows(sqlmock.NewRows([]string{"peer_id", "last_message_id"}).AddRow(90, 99))
			got, err := s.ListMyDirectConversations(ctx, &pb.ListMyDirectConversationsRequest{SnapshotUpperMessageId: 100})
			if err != nil || len(got.GetConversations()) != 1 {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
}
func TestDirectDirectoryDoesNotReturnPartialPage(t *testing.T) {
	s, m := testIMServer(t)
	m.ExpectQuery(regexp.QuoteMeta(testDirectDirectorySQL)).WithArgs(int64(42), int64(42), int64(100), int64(42), int64(42), int64(100), int64(0), int64(0), 21).WillReturnRows(sqlmock.NewRows([]string{"peer_id", "last_message_id"}).AddRow(43, 99).AddRow(44, 98).RowError(1, errors.New("private read failure")))
	got, err := s.ListMyDirectConversations(teamGroupListContext(t), &pb.ListMyDirectConversationsRequest{SnapshotUpperMessageId: 100})
	if got != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private read failure" {
		t.Fatalf("%v %v", got, err)
	}
}
