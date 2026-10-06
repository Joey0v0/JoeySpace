package main

import (
	"context"
	"errors"
	"net"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const checkSourceQuery = "SELECT messages.id FROM `messages` JOIN `groups` ON `groups`.id = messages.to_id WHERE messages.id = ? AND messages.to_id = ? AND messages.chat_type = ? AND `groups`.team_id = ? LIMIT ?"

func TestCheckTeamGroupMessageOverRPC(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		if req.GetTeamId() != 200 {
			t.Fatalf("checked team %d", req.GetTeamId())
		}
		md, _ := metadata.FromOutgoingContext(ctx)
		if len(md.Get("authorization")) != 1 {
			t.Fatal("authorization was not forwarded")
		}
		return nil
	})
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	expectTeamGroupReadFence(mock, 300, 42, 200, nil, true)
	mock.ExpectQuery(regexp.QuoteMeta(checkSourceQuery)).WithArgs(int64(400), int64(300), 2, int64(200), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(400)))
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
	result, err := pb.NewIMClient(conn).CheckTeamGroupMessage(ctx, &pb.CheckTeamGroupMessageRequest{TeamId: 200, GroupId: 300, MessageId: 400})
	if err != nil || result == nil {
		t.Fatalf("check message: %v %v", result, err)
	}
}

func TestCheckTeamGroupMessageRejectsInvalidAndUnauthorized(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error {
		return status.Error(codes.PermissionDenied, "left team")
	})
	for _, req := range []*pb.CheckTeamGroupMessageRequest{nil, {TeamId: 0, GroupId: 300, MessageId: 400}, {TeamId: 200, GroupId: 0, MessageId: 400}, {TeamId: 200, GroupId: 300, MessageId: 0}} {
		if _, err := s.CheckTeamGroupMessage(historyContext(t), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request %v: %v", req, err)
		}
	}
	if _, err := s.CheckTeamGroupMessage(context.Background(), &pb.CheckTeamGroupMessageRequest{TeamId: 200, GroupId: 300, MessageId: 400}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
	if _, err := s.CheckTeamGroupMessage(historyContext(t), &pb.CheckTeamGroupMessageRequest{TeamId: 200, GroupId: 300, MessageId: 400}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("nonmember: %v", err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
	if _, err := s.CheckTeamGroupMessage(historyContext(t), &pb.CheckTeamGroupMessageRequest{TeamId: 200, GroupId: 300, MessageId: 400}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("left team: %v", err)
	}
}

func TestCheckTeamGroupMessageRejectsWrongSourceAndDatabaseFailure(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	for _, queryErr := range []error{nil, errors.New("private database detail")} {
		mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(int64(200)))
		expectTeamGroupReadFence(mock, 300, 42, 200, nil, true)
		expectation := mock.ExpectQuery(regexp.QuoteMeta(checkSourceQuery)).WithArgs(int64(400), int64(300), 2, int64(200), 1)
		want := codes.NotFound
		if queryErr == nil {
			expectation.WillReturnRows(sqlmock.NewRows([]string{"id"}))
		} else {
			expectation.WillReturnError(queryErr)
			want = codes.Unavailable
		}
		_, err := s.CheckTeamGroupMessage(historyContext(t), &pb.CheckTeamGroupMessageRequest{TeamId: 200, GroupId: 300, MessageId: 400})
		if status.Code(err) != want || status.Convert(err).Message() == "private database detail" {
			t.Fatalf("source check %v: %v", queryErr, err)
		}
	}
}
