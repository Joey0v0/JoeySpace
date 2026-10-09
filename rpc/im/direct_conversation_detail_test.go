package main

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"regexp"
	"testing"
)

const testDirectDetailSQL = "SELECT COALESCE(MAX(id), 0) AS last_message_id FROM messages WHERE chat_type = 1 AND ((from_id = ? AND to_id = ?) OR (from_id = ? AND to_id = ?))"

func TestDirectConversationPersistentPair(t *testing.T) {
	for _, last := range []int64{0, 9007199254740999} {
		s, m := testIMServer(t)
		m.ExpectQuery(regexp.QuoteMeta(testDirectDetailSQL)).WithArgs(int64(42), int64(43), int64(43), int64(42)).WillReturnRows(sqlmock.NewRows([]string{"last_message_id"}).AddRow(last))
		got, err := s.GetMyDirectConversation(teamGroupListContext(t), &pb.GetMyDirectConversationRequest{PeerId: 43})
		if last == 0 {
			if got != nil || status.Code(err) != codes.NotFound {
				t.Fatalf("%v %v", got, err)
			}
		} else if err != nil || got.GetConversation().GetPeerId() != 43 || got.GetConversation().GetLastMessageId() != last {
			t.Fatalf("%v %v", got, err)
		}
	}
}
func TestDirectConversationRejectsInvalidPeer(t *testing.T) {
	s, _ := testIMServer(t)
	for _, id := range []int64{-1, 0, 42} {
		got, err := s.GetMyDirectConversation(teamGroupListContext(t), &pb.GetMyDirectConversationRequest{PeerId: id})
		if got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("%v %v", got, err)
		}
	}
}

func TestDirectConversationFailureAndMissingIdentity(t *testing.T) {
	s, m := testIMServer(t)
	got, err := s.GetMyDirectConversation(context.Background(), &pb.GetMyDirectConversationRequest{PeerId: 43})
	if got != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("%v %v", got, err)
	}
	m.ExpectQuery(regexp.QuoteMeta(testDirectDetailSQL)).WithArgs(int64(42), int64(43), int64(43), int64(42)).WillReturnError(errors.New("private error"))
	got, err = s.GetMyDirectConversation(teamGroupListContext(t), &pb.GetMyDirectConversationRequest{PeerId: 43})
	if got != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private error" {
		t.Fatalf("%v %v", got, err)
	}
}
