package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/im/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const offlineMessagesQuery = "SELECT messages.id, messages.msg_id, messages.from_id, messages.sender_type, messages.initiator_id, messages.to_id, messages.chat_type, messages.content_type, messages.content, messages.created_at FROM `messages` JOIN offline_messages ON offline_messages.message_id = messages.id WHERE offline_messages.user_id = ? ORDER BY messages.id ASC"

func TestListOfflineMessagesRPCIsReadOnlyAndScopedToTokenUser(t *testing.T) {
	s, mock := testIMServer(t)
	now := time.Date(2026, 9, 29, 12, 0, 0, 123000000, time.UTC)
	cardContent, err := model.EncodeTaskCreatedCard(9007199254740993, "已确认标题")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		mock.ExpectQuery(regexp.QuoteMeta(offlineMessagesQuery)).WithArgs(int64(42)).
			WillReturnRows(sqlmock.NewRows([]string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "to_id", "chat_type", "content_type", "content", "created_at"}).
				AddRow(int64(9007199254740993), "m1", int64(9007199254740994), 2, int64(9007199254740995), int64(300), 2, model.MessageContentTaskCard, cardContent, now))
		mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(nil))
		mock.ExpectQuery("SELECT message_id, mentioned_user_id FROM `im_group_message_mentions`").WithArgs(int64(9007199254740993)).
			WillReturnRows(sqlmock.NewRows([]string{"message_id", "mentioned_user_id"}))
	}
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
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
	for i := 0; i < 2; i++ {
		result, err := pb.NewIMClient(conn).ListOfflineMessages(ctx, &pb.ListOfflineMessagesRequest{})
		if err != nil || len(result.GetMessages()) != 1 {
			t.Fatalf("pull %d: %v %v", i, result, err)
		}
		message := result.GetMessages()[0]
		if message.GetId() != 9007199254740993 || message.GetFromId() != 9007199254740994 || message.GetSenderType() != 2 || message.GetInitiatorId() != 9007199254740995 || message.GetMsgId() != "m1" || !message.GetCreatedAt().AsTime().Equal(now) {
			t.Fatalf("pull %d returned %+v", i, message)
		}
		if message.GetToId() != 300 || message.GetContentType() != int32(model.MessageContentTaskCard) || message.GetContent() != cardContent {
			t.Fatalf("pull %d lost card content: %+v", i, message)
		}
	}
}

func TestListOfflineMessagesRejectsInvalidTokenBeforeQuery(t *testing.T) {
	s, _ := testIMServer(t)
	for _, ctx := range []context.Context{
		context.Background(),
		metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer invalid")),
	} {
		result, err := s.ListOfflineMessages(ctx, &pb.ListOfflineMessagesRequest{})
		if result != nil || status.Code(err) != codes.Unauthenticated {
			t.Fatalf("result=%v error=%v", result, err)
		}
	}
}

func TestListOfflineMessagesHidesDatabaseError(t *testing.T) {
	s, mock := testIMServer(t)
	mock.ExpectQuery(regexp.QuoteMeta(offlineMessagesQuery)).WithArgs(int64(42)).WillReturnError(errors.New("private database detail"))
	result, err := s.ListOfflineMessages(historyContext(t), &pb.ListOfflineMessagesRequest{})
	if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private database detail" {
		t.Fatalf("result=%v error=%v", result, err)
	}
}

func TestAckOfflineMessagesRPCOnlyDeletesCurrentUserRecords(t *testing.T) {
	s, mock := testIMServer(t)
	for _, affected := range []int64{2, 0} {
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `offline_messages` WHERE user_id = ? AND message_id IN (?,?)")).
			WithArgs(int64(42), int64(9007199254740993), int64(9007199254740994)).
			WillReturnResult(sqlmock.NewResult(0, affected))
		mock.ExpectCommit()
	}
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
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
	for i := 0; i < 2; i++ {
		_, err := pb.NewIMClient(conn).AckOfflineMessages(ctx, &pb.AckOfflineMessagesRequest{
			MessageIds: []int64{9007199254740993, 9007199254740994},
		})
		if err != nil {
			t.Fatalf("ack %d: %v", i, err)
		}
	}
}

func TestAckOfflineMessagesRejectsInvalidRequestBeforeDelete(t *testing.T) {
	s, _ := testIMServer(t)
	validCtx := historyContext(t)
	for _, ids := range [][]int64{nil, {0}, {-1}, make([]int64, 1001)} {
		result, err := s.AckOfflineMessages(validCtx, &pb.AckOfflineMessagesRequest{MessageIds: ids})
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("IDs=%d: %v %v", len(ids), result, err)
		}
	}
	result, err := s.AckOfflineMessages(context.Background(), &pb.AckOfflineMessagesRequest{MessageIds: []int64{1}})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v %v", result, err)
	}
}

func TestAckOfflineMessagesHidesDatabaseError(t *testing.T) {
	s, mock := testIMServer(t)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM `offline_messages` WHERE user_id = ? AND message_id IN (?)")).
		WithArgs(int64(42), int64(7)).WillReturnError(errors.New("private database detail"))
	mock.ExpectRollback()
	result, err := s.AckOfflineMessages(historyContext(t), &pb.AckOfflineMessagesRequest{MessageIds: []int64{7}})
	if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private database detail" {
		t.Fatalf("result=%v error=%v", result, err)
	}
}
