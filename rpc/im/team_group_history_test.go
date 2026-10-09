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
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const historyGroupQuery = "SELECT `team_id` FROM `groups` WHERE id = ? LIMIT ?"
const historyMessagesQuery = "SELECT id, msg_id, from_id, sender_type, initiator_id, content_type, content, created_at FROM `messages` WHERE (to_id = ? AND chat_type = ?) AND id < ? ORDER BY id DESC LIMIT ?"

func historyContext(t *testing.T) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
}

func TestListTeamGroupMessagesPage(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(_ context.Context, req *userpb.CheckTeamMemberRequest) error {
		if req.GetTeamId() != 200 {
			t.Fatalf("checked team %d", req.GetTeamId())
		}
		return nil
	})
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	expectTeamGroupReadFence(mock, 300, 42, 200, nil, true)
	mock.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	cardContent, err := model.EncodeTaskCreatedCard(9007199254740993, "已确认标题")
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(historyMessagesQuery)).WithArgs(int64(300), 2, int64(600), 3).
		WillReturnRows(sqlmock.NewRows([]string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "content_type", "content", "created_at"}).
			AddRow(int64(500), "msg-500", int64(42), 2, int64(9007199254740995), model.MessageContentTaskCard, cardContent, now).
			AddRow(int64(490), "msg-490", int64(43), 1, 0, int8(1), "world", now).
			AddRow(int64(480), "msg-480", int64(42), 1, 0, int8(1), "older", now))
	mock.ExpectQuery("SELECT message_id, mentioned_user_id FROM `im_group_message_mentions`").WithArgs(int64(500), int64(490)).
		WillReturnRows(sqlmock.NewRows([]string{"message_id", "mentioned_user_id"}).AddRow(int64(490), int64(42)))
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
	result, err := pb.NewIMClient(conn).ListTeamGroupMessages(ctx, &pb.ListTeamGroupMessagesRequest{TeamId: 200, GroupId: 300, BeforeMessageId: 600, Limit: 2})
	if err != nil || len(result.GetMessages()) != 2 || result.GetMessages()[0].GetId() != 500 || result.GetMessages()[0].GetCreatedAtUnixMs() != now.UnixMilli() || result.GetNextBeforeMessageId() != 490 {
		t.Fatalf("history page: %v %v", result, err)
	}
	if result.GetMessages()[0].GetSenderType() != 2 || result.GetMessages()[0].GetInitiatorId() != 9007199254740995 || result.GetMessages()[1].GetSenderType() != 1 || result.GetMessages()[1].GetInitiatorId() != 0 {
		t.Fatalf("history lost sender identity: %v", result)
	}
	if result.GetMessages()[0].GetContentType() != int32(model.MessageContentTaskCard) || result.GetMessages()[0].GetContent() != cardContent {
		t.Fatalf("history lost card content: %v", result)
	}
	if len(result.GetMessages()[0].GetMentionedUserIds()) != 0 || len(result.GetMessages()[1].GetMentionedUserIds()) != 1 || result.GetMessages()[1].GetMentionedUserIds()[0] != 42 {
		t.Fatalf("history mention relation lost: %v", result)
	}
}

func TestListTeamGroupMessagesChecksBothMembershipsBeforeReading(t *testing.T) {
	for _, tc := range []struct {
		name       string
		memberTeam any
		teamErr    error
		want       codes.Code
	}{
		{"not a group member", nil, nil, codes.PermissionDenied},
		{"left team", int64(200), status.Error(codes.PermissionDenied, "left team"), codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return tc.teamErr })
			rows := sqlmock.NewRows([]string{"team_id"})
			if tc.memberTeam != nil {
				rows.AddRow(tc.memberTeam)
			}
			mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(rows)
			result, err := s.ListTeamGroupMessages(historyContext(t), &pb.ListTeamGroupMessagesRequest{TeamId: 200, GroupId: 300})
			if result != nil || status.Code(err) != tc.want {
				t.Fatalf("history: %v %v", result, err)
			}
		})
	}
}

func TestListTeamGroupMessagesRejectsOtherTeamAndLegacyGroup(t *testing.T) {
	for _, teamID := range []any{nil, int64(201)} {
		s, mock := testIMServer(t)
		s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
		mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(teamID))
		if teamID != nil {
			expectTeamGroupReadFence(mock, 300, 42, teamID.(int64), nil, true)
		}
		mock.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(teamID))
		result, err := s.ListTeamGroupMessages(historyContext(t), &pb.ListTeamGroupMessagesRequest{TeamId: 200, GroupId: 300})
		if result != nil || status.Code(err) != codes.NotFound {
			t.Fatalf("team %v: %v %v", teamID, result, err)
		}
	}
}

func TestListTeamGroupMessagesRejectsInvalidInputAndDatabaseFailure(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	for _, req := range []*pb.ListTeamGroupMessagesRequest{nil, {TeamId: 0, GroupId: 300}, {TeamId: 200, GroupId: 0}, {TeamId: 200, GroupId: 300, BeforeMessageId: -1}, {TeamId: 200, GroupId: 300, Limit: 101}} {
		result, err := s.ListTeamGroupMessages(historyContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %v %v", result, err)
		}
	}
	result, err := s.ListTeamGroupMessages(context.Background(), &pb.ListTeamGroupMessagesRequest{TeamId: 200, GroupId: 300})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v %v", result, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	expectTeamGroupReadFence(mock, 300, 42, 200, nil, true)
	mock.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, msg_id, from_id, sender_type, initiator_id, content_type, content, created_at FROM `messages` WHERE to_id = ? AND chat_type = ? ORDER BY id DESC LIMIT ?")).
		WithArgs(int64(300), 2, 21).WillReturnError(errors.New("private database detail"))
	result, err = s.ListTeamGroupMessages(historyContext(t), &pb.ListTeamGroupMessagesRequest{TeamId: 200, GroupId: 300})
	if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private database detail" {
		t.Fatalf("database failure: %v %v", result, err)
	}
}
