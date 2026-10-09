package main

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
)

func TestListMyTaskNotificationsUnreadFirstSnapshotAndAuthorizesTwice(t *testing.T) {
	s, mock := testTaskServer(t)
	identities, directories := 0, 0
	s.identityClient = taskIdentityFake(func(context.Context) (*userpb.GetUserInfoResponse, error) {
		identities++
		return &userpb.GetUserInfoResponse{Id: 42}, nil
	})
	s.teamDirectory = taskDirectoryFake(func(_ context.Context, req *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		directories++
		if req.GetAfterTeamId() != 0 || req.GetLimit() != 100 {
			t.Fatalf("directory request=%v", req)
		}
		return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 200}, {TeamId: 300}}}, nil
	})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT MAX(n.id) AS upper_notification_id FROM task_status_notifications AS n WHERE n.recipient_id = ? AND n.team_id IN (?,?)")).WithArgs(int64(42), int64(200), int64(300)).WillReturnRows(sqlmock.NewRows([]string{"upper_notification_id"}).AddRow(99))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM task_status_notifications AS n").WithArgs(int64(42), int64(200), int64(300)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("SELECT n.id AS notification_id.*JOIN tasks AS t.*ORDER BY n.read_at IS NULL DESC,n.id DESC").WithArgs(int64(42), int64(200), int64(300), int64(99), 3).WillReturnRows(sqlmock.NewRows([]string{"notification_id", "team_id", "task_id", "task_title", "current_status", "actor_id", "from_status", "to_status", "created_at_unix_ms", "read_at_unix_ms"}).
		AddRow(99, 200, 70, "发布", 1, 8, 0, 1, 1790874000000, 0).
		AddRow(98, 300, 71, "复盘", 2, 9, 1, 2, 1790874000001, 1790874000002))
	result, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.GetNotifications()) != 2 || result.GetUnreadCount() != 2 || result.GetNextCursor() != "" || result.Notifications[0].GetTeamId() != 200 || result.Notifications[0].GetTaskTitle() != "发布" || identities != 2 || directories != 2 {
		t.Fatalf("result=%v identity=%d directories=%d", result, identities, directories)
	}
}

func TestNotificationCursorBindsTeamAndRejectsMalformed(t *testing.T) {
	if _, err := decodeNotificationCursor("not-json", 0); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	encoded := encodeNotificationCursor(notificationCursor{Version: 1, TeamID: 7, UpperID: 10, Phase: "unread", LastID: 9})
	if _, err := decodeNotificationCursor(encoded, 8); err == nil {
		t.Fatal("cross-team cursor accepted")
	}
}
