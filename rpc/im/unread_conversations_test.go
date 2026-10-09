package main

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func overviewRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"chat_type", "team_id", "group_id", "peer_id", "group_name", "last_message_id", "unread_count", "mention_unread_count", "closed_generation", "content", "content_type", "created_at"})
}

func TestListMyUnreadConversationsSkipsRevokedCandidatesWithoutLosingPage(t *testing.T) {
	s, m := testIMServer(t)
	teamCalls := map[int64]int{}
	s.teamClient = teamCheckFunc(func(_ context.Context, req *userpb.CheckTeamMemberRequest) error {
		teamCalls[req.TeamId]++
		if req.TeamId == 100 {
			return status.Error(codes.PermissionDenied, "left team")
		}
		return nil
	})
	now := time.Unix(100, 0)
	m.ExpectQuery("SELECT c.chat_type").
		WithArgs(int64(42), int64(42), int64(42), int64(1000), int64(42), int64(42), int64(42), int64(42), int64(42), int64(42), int64(42), int64(42), int64(1000), int64(42), int64(42), 0, int64(0), int64(0), 51).
		WillReturnRows(overviewRows().
			AddRow(2, 100, 10, 0, "private", 99, 4, 1, 0, "secret", 1, now).
			AddRow(1, 0, 0, 77, "", 98, 2, 0, 0, "hello", 1, now).
			AddRow(2, 200, 20, 0, "工作群", 97, 1, 1, 0, "visible", 1, now))
	got, err := s.ListMyUnreadConversations(teamGroupListContext(t), &pb.ListMyUnreadConversationsRequest{SnapshotUpperMessageId: 1000, Limit: 1})
	if err != nil || len(got.GetConversations()) != 1 || got.GetConversations()[0].GetPeerId() != 77 || got.GetNextBeforeLastMessageId() != 98 {
		t.Fatalf("page after revoked group: %v %v", got, err)
	}
	if teamCalls[100] != 1 || teamCalls[200] != 1 {
		t.Fatalf("team calls: %v", teamCalls)
	}
}

func TestListMyUnreadConversationsGroupFenceAndFinalRecheck(t *testing.T) {
	s, m := testIMServer(t)
	calls := 0
	s.teamClient = directoryTeamClient{generation: func() int64 { calls++; return 2 }}
	now := time.Unix(100, 0)
	m.ExpectQuery("SELECT c.chat_type").WillReturnRows(overviewRows().
		AddRow(2, 200, 20, 0, "工作群", 98, 2, 1, 1, "visible", 1, now))
	m.ExpectQuery("SELECT gm.group_id, g.team_id").WithArgs(int64(42), int64(20)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "team_id", "closed_generation"}).AddRow(20, 200, 1))
	got, err := s.ListMyUnreadConversations(teamGroupListContext(t), &pb.ListMyUnreadConversationsRequest{SnapshotUpperMessageId: 1000})
	if err != nil || len(got.GetConversations()) != 1 || got.GetConversations()[0].GetGroupId() != 20 || calls != 2 {
		t.Fatalf("group result: %v %v calls=%d", got, err, calls)
	}
}

func TestListMyUnreadConversationsScansPastFullRevokedBatch(t *testing.T) {
	s, m := testIMServer(t)
	calls := 0
	s.teamClient = teamCheckFunc(func(_ context.Context, req *userpb.CheckTeamMemberRequest) error {
		calls++
		if req.TeamId != 100 {
			t.Fatalf("unexpected team %d", req.TeamId)
		}
		return status.Error(codes.PermissionDenied, "left")
	})
	now := time.Unix(100, 0)
	rows := overviewRows()
	for id := 100; id >= 50; id-- {
		rows.AddRow(2, 100, id, 0, "撤权群", id, 1, 0, 0, "revoked", 1, now)
	}
	m.ExpectQuery("SELECT c.chat_type").WillReturnRows(rows)
	m.ExpectQuery("SELECT c.chat_type").
		WithArgs(int64(42), int64(42), int64(42), int64(1000), int64(42), int64(42), int64(42), int64(42), int64(42), int64(42), int64(42), int64(42), int64(1000), int64(42), int64(42), 0, int64(50), int64(50), 51).
		WillReturnRows(overviewRows().AddRow(1, 0, 0, 77, "", 49, 1, 0, 0, "direct", 1, now))
	got, err := s.ListMyUnreadConversations(teamGroupListContext(t), &pb.ListMyUnreadConversationsRequest{SnapshotUpperMessageId: 1000, Limit: 1})
	if err != nil || len(got.GetConversations()) != 1 || got.GetConversations()[0].GetPeerId() != 77 || calls != 1 {
		t.Fatalf("scan result: %v %v calls=%d", got, err, calls)
	}
}

func TestListMyUnreadConversationsDiscardsPageOnFinalFenceChange(t *testing.T) {
	s, m := testIMServer(t)
	s.teamClient = directoryTeamClient{generation: func() int64 { return 2 }}
	m.ExpectQuery("SELECT c.chat_type").WillReturnRows(overviewRows().
		AddRow(2, 200, 20, 0, "工作群", 98, 2, 0, 0, "private", 1, time.Unix(100, 0)))
	m.ExpectQuery("SELECT gm.group_id, g.team_id").
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "team_id", "closed_generation"}).AddRow(20, 200, 2))
	got, err := s.ListMyUnreadConversations(teamGroupListContext(t), &pb.ListMyUnreadConversationsRequest{SnapshotUpperMessageId: 1000})
	if got != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("changed fence: %v %v", got, err)
	}
}

func TestListMyUnreadConversationsRejectsInvalidScope(t *testing.T) {
	s, _ := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	for _, req := range []*pb.ListMyUnreadConversationsRequest{{Limit: 51}, {Limit: -1}, {SnapshotUpperMessageId: -1}, {BeforeLastMessageId: 1}} {
		if got, err := s.ListMyUnreadConversations(teamGroupListContext(t), req); got != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid: %v %v", got, err)
		}
	}
	if got, err := s.ListMyUnreadConversations(context.Background(), &pb.ListMyUnreadConversationsRequest{MentionsOnly: true}); got != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("identity: %v %v", got, err)
	}
}

func TestListMyUnreadMentionsUsesVerifiedRelationsAndCurrentGroupScope(t *testing.T) {
	s, m := testIMServer(t)
	calls := 0
	s.teamClient = directoryTeamClient{generation: func() int64 { calls++; return 2 }}
	m.ExpectQuery("SELECT c.chat_type").
		WithArgs(int64(42), int64(42), int64(42), int64(1000), int64(42), int64(42), int64(42), int64(42),
			int64(42), int64(42), int64(42), int64(42), int64(1000), int64(42), int64(42), 1, int64(0), int64(0), 51).
		WillReturnRows(overviewRows().AddRow(2, 200, 20, 0, "工作群", 98, 3, 1, 1, "@me 看一下", 1, time.Unix(100, 0)))
	m.ExpectQuery("SELECT gm.group_id, g.team_id").WithArgs(int64(42), int64(20)).
		WillReturnRows(sqlmock.NewRows([]string{"group_id", "team_id", "closed_generation"}).AddRow(20, 200, 1))
	got, err := s.ListMyUnreadConversations(teamGroupListContext(t), &pb.ListMyUnreadConversationsRequest{SnapshotUpperMessageId: 1000, MentionsOnly: true})
	if err != nil || len(got.GetConversations()) != 1 || got.GetConversations()[0].GetMentionUnreadCount() != 1 || got.GetConversations()[0].GetGroupName() != "工作群" || calls != 2 {
		t.Fatalf("mention page=%v error=%v team checks=%d", got, err, calls)
	}
}
