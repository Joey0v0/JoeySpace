package main

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func offlineAccessRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "msg_id", "from_id", "sender_type", "initiator_id", "to_id", "chat_type", "content_type", "content", "created_at"})
}

func TestOfflinePullFiltersRevokedGroupsAndRechecksAfterRejoin(t *testing.T) {
	s, mock := testIMServer(t)
	teamChecks := 0
	ctx := historyContext(t)
	incoming, _ := metadata.FromIncomingContext(ctx)
	authorization := incoming.Get("authorization")[0]
	s.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != authorization {
			t.Fatal("team check lost current scope or login token")
		}
		teamChecks++
		if teamChecks == 2 {
			return status.Error(codes.PermissionDenied, "left team")
		}
		return nil
	})
	for pull := 0; pull < 3; pull++ {
		// The same stored rows are returned again: pulls never delete deliveries.
		mock.ExpectQuery(regexp.QuoteMeta(offlineMessagesQuery)).WithArgs(int64(42)).WillReturnRows(offlineAccessRows().
			AddRow(10, "direct", 7, 0, 0, 42, 1, 1, "direct content", time.Now()).
			AddRow(11, "old-group", 7, 1, 0, 300, 2, 1, "allowed old group", time.Now()).
			AddRow(12, "left-group", 7, 1, 0, 301, 2, 1, "hidden left group", time.Now()).
			AddRow(13, "team", 7, 1, 0, 302, 2, 1, "team content", time.Now()).
			AddRow(14, "team-bot", 7, 2, 42, 302, 2, 1, "bot content", time.Now()))
		mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(nil))
		mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(301), int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}))
		mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(302), int64(42), 1).
			WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
		result, err := s.ListOfflineMessages(ctx, &pb.ListOfflineMessagesRequest{})
		want := 4
		if pull == 1 {
			want = 2
		}
		if err != nil || len(result.GetMessages()) != want || result.GetMessages()[0].GetMsgId() != "direct" || result.GetMessages()[1].GetMsgId() != "old-group" {
			t.Fatalf("pull %d: result=%v err=%v", pull, result, err)
		}
		if pull != 1 && (result.GetMessages()[2].GetMsgId() != "team" || result.GetMessages()[3].GetSenderType() != 2) {
			t.Fatal("rejoined team lost retained user or bot delivery")
		}
	}
	if teamChecks != 3 {
		t.Fatalf("checked team %d times; must reuse only within each pull", teamChecks)
	}
}

func TestOfflinePullAuthorizationFailuresReturnNoPartialContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		teamErr error
		dbErr   error
		noTeam  bool
		want    codes.Code
	}{
		{"group database", nil, errors.New("private database detail"), false, codes.Unavailable},
		{"team unavailable", status.Error(codes.Unavailable, "private upstream"), nil, false, codes.Unavailable},
		{"team unexpected", status.Error(codes.Internal, "private upstream"), nil, false, codes.Unavailable},
		{"login invalidated", status.Error(codes.Unauthenticated, "login invalidated"), nil, false, codes.Unauthenticated},
		{"timeout", status.Error(codes.DeadlineExceeded, "timeout"), nil, false, codes.DeadlineExceeded},
		{"canceled", status.Error(codes.Canceled, "canceled"), nil, false, codes.Canceled},
		{"team not configured", nil, nil, true, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			if !tc.noTeam {
				s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return tc.teamErr })
			}
			mock.ExpectQuery(regexp.QuoteMeta(offlineMessagesQuery)).WithArgs(int64(42)).WillReturnRows(offlineAccessRows().
				AddRow(10, "direct", 7, 1, 0, 42, 1, 1, "must not return partial content", time.Now()).
				AddRow(11, "team", 7, 1, 0, 300, 2, 1, "unchecked content", time.Now()))
			query := mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1)
			if tc.dbErr != nil {
				query.WillReturnError(tc.dbErr)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
			}
			result, err := s.ListOfflineMessages(historyContext(t), &pb.ListOfflineMessagesRequest{})
			if result != nil || status.Code(err) != tc.want || status.Convert(err).Message() == "private database detail" || status.Convert(err).Message() == "private upstream" {
				t.Fatalf("result=%v err=%v", result, err)
			}
		})
	}
}
