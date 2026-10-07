package main

import (
	"context"
	"net"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func directContext(t *testing.T) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t), "user_id", "999"))
}

const directCountQuery = `SELECT COUNT(*) FROM messages
WHERE messages.chat_type = 1 AND messages.from_id = ? AND messages.to_id = ?
AND NOT EXISTS (SELECT 1 FROM im_direct_message_reads AS r
WHERE r.user_id = ? AND r.peer_id = ? AND r.message_id = messages.id)`

func expectDirectCount(mock sqlmock.Sqlmock, count int64) {
	mock.ExpectQuery(regexp.QuoteMeta(directCountQuery)).WithArgs(int64(43), int64(42), int64(42), int64(43)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
}

func TestDirectHistoryScopesBothDirectionsAndPages(t *testing.T) {
	s, mock := testIMServer(t)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	query := "SELECT id, msg_id, from_id, to_id, content_type, content, created_at FROM `messages` WHERE (chat_type = ? AND ((from_id = ? AND to_id = ?) OR (from_id = ? AND to_id = ?))) AND id < ? ORDER BY id DESC LIMIT ?"
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(1, int64(42), int64(43), int64(43), int64(42), int64(600), 3).
		WillReturnRows(sqlmock.NewRows([]string{"id", "msg_id", "from_id", "to_id", "content_type", "content", "created_at"}).
			AddRow(500, "m500", 43, 42, 1, "received", now).
			AddRow(490, "m490", 42, 43, 1, "sent", now).
			AddRow(480, "m480", 43, 42, 1, "older", now))
	page, err := s.ListDirectMessages(directContext(t), &pb.ListDirectMessagesRequest{PeerId: 43, BeforeMessageId: 600, Limit: 2})
	if err != nil || len(page.GetMessages()) != 2 || page.GetMessages()[0].GetFromId() != 43 || page.GetMessages()[1].GetToId() != 43 || page.GetNextBeforeMessageId() != 490 || page.GetMessages()[0].GetCreatedAtUnixMs() != now.UnixMilli() {
		t.Fatalf("direct history page=%v err=%v", page, err)
	}
}

func TestDirectUnreadExplicitReadReplayAndLateMessage(t *testing.T) {
	s, mock := testIMServer(t)
	ctx := directContext(t)
	expectDirectCount(mock, 2)
	first, err := s.GetDirectUnread(ctx, &pb.GetDirectUnreadRequest{PeerId: 43})
	if err != nil || first.GetUnreadCount() != 2 {
		t.Fatalf("initial count=%v err=%v", first, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		mock.ExpectBegin()
		lock := "SELECT `id` FROM `messages` WHERE chat_type = ? AND from_id = ? AND to_id = ? AND id IN (?,?) ORDER BY id ASC FOR SHARE"
		mock.ExpectQuery(regexp.QuoteMeta(lock)).WithArgs(1, int64(43), int64(42), int64(100), int64(101)).
			WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(100).AddRow(101))
		insert := "INSERT INTO `im_direct_message_reads` (`user_id`,`peer_id`,`message_id`) VALUES (?,?,?),(?,?,?) ON DUPLICATE KEY UPDATE `user_id`=`user_id`"
		mock.ExpectExec(regexp.QuoteMeta(insert)).WithArgs(int64(42), int64(43), int64(100), int64(42), int64(43), int64(101)).WillReturnResult(sqlmock.NewResult(0, 2))
		mock.ExpectCommit()
		expectDirectCount(mock, 1)
		marked, err := s.MarkDirectMessagesRead(ctx, &pb.MarkDirectMessagesReadRequest{PeerId: 43, MessageIds: []int64{101, 100, 101}})
		if err != nil || marked.GetUnreadCount() != 1 || !reflect.DeepEqual(marked.GetMessageIds(), []int64{100, 101}) {
			t.Fatalf("read attempt=%d result=%v err=%v", attempt, marked, err)
		}
	}
	expectDirectCount(mock, 2) // A lower-ID message may arrive after the first read.
	late, err := s.GetDirectUnread(ctx, &pb.GetDirectUnreadRequest{PeerId: 43})
	if err != nil || late.GetUnreadCount() != 2 {
		t.Fatalf("late count=%v err=%v", late, err)
	}
}

func TestDirectReadRejectsWrongScopeAsOneBatch(t *testing.T) {
	s, mock := testIMServer(t)
	mock.ExpectBegin()
	lock := "SELECT `id` FROM `messages` WHERE chat_type = ? AND from_id = ? AND to_id = ? AND id IN (?,?) ORDER BY id ASC FOR SHARE"
	mock.ExpectQuery(regexp.QuoteMeta(lock)).WithArgs(1, int64(43), int64(42), int64(100), int64(102)).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(100))
	mock.ExpectRollback()
	result, err := s.MarkDirectMessagesRead(directContext(t), &pb.MarkDirectMessagesReadRequest{PeerId: 43, MessageIds: []int64{100, 102}})
	if result != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("wrong-scope read=%v err=%v", result, err)
	}
}

func TestDirectReadRejectsInvalidParametersBeforeSQL(t *testing.T) {
	s, _ := testIMServer(t)
	ctx := directContext(t)
	for _, peer := range []int64{0, -1, 42} {
		if _, err := s.GetDirectUnread(ctx, &pb.GetDirectUnreadRequest{PeerId: peer}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("peer %d: %v", peer, err)
		}
	}
	for _, ids := range [][]int64{nil, {0}, {-1}, make([]int64, 101)} {
		if _, err := s.MarkDirectMessagesRead(ctx, &pb.MarkDirectMessagesReadRequest{PeerId: 43, MessageIds: ids}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("IDs %v: %v", ids, err)
		}
	}
	if _, err := s.ListDirectMessages(ctx, &pb.ListDirectMessagesRequest{PeerId: 43, Limit: 101}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unbounded history: %v", err)
	}
}

func TestDirectUnreadRequiresTokenIdentity(t *testing.T) {
	s, _ := testIMServer(t)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("user_id", "42"))
	if _, err := s.GetDirectUnread(ctx, &pb.GetDirectUnreadRequest{PeerId: 43}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("untrusted metadata must not identify the reader: %v", err)
	}
	if _, err := s.ListDirectMessages(ctx, &pb.ListDirectMessagesRequest{PeerId: 43}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("history must require a Token: %v", err)
	}
}

func TestDirectUnreadRegisteredOnIMRPC(t *testing.T) {
	s, mock := testIMServer(t)
	expectDirectCount(mock, 3)
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
	t.Cleanup(func() { conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
	result, err := pb.NewIMClient(conn).GetDirectUnread(ctx, &pb.GetDirectUnreadRequest{PeerId: 43})
	if err != nil || result.GetPeerId() != 43 || result.GetUnreadCount() != 3 {
		t.Fatalf("registered RPC=%v err=%v", result, err)
	}
}
