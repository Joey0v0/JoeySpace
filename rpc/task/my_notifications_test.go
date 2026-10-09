package main

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	mock.ExpectQuery("SELECT MAX\\(n.id\\).*CURRENT_TIMESTAMP.*read_cutoff_unix_ms").WithArgs(int64(42), int64(200), int64(300)).WillReturnRows(sqlmock.NewRows([]string{"upper_notification_id", "read_cutoff_unix_ms"}).AddRow(99, 1790873999999))
	mock.ExpectQuery("SELECT count\\(\\*\\) FROM task_status_notifications AS n").WithArgs(int64(42), int64(200), int64(300)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("SELECT n.id AS notification_id.*JOIN tasks AS t.*ORDER BY \\(n.read_at IS NULL OR .* > 1790873999999\\) DESC,n.id DESC").WithArgs(int64(42), int64(200), int64(300), int64(99), 3).WillReturnRows(sqlmock.NewRows([]string{"notification_id", "team_id", "task_id", "task_title", "current_status", "actor_id", "from_status", "to_status", "created_at_unix_ms", "read_at_unix_ms"}).
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

func TestNotificationPaginationKeepsSnapshotPartitionAfterRead(t *testing.T) {
	s, mock := testTaskServer(t)
	const currentSecond = int64(1790874000000)
	const cutoff = currentSecond - 1
	s.identityClient = taskIdentityFake(func(context.Context) (*userpb.GetUserInfoResponse, error) {
		return &userpb.GetUserInfoResponse{Id: 42}, nil
	})
	s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		return &userpb.ListMyTeamsResponse{}, nil
	})
	s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	}}
	columns := []string{"notification_id", "team_id", "task_id", "task_title", "current_status", "actor_id", "from_status", "to_status", "created_at_unix_ms", "read_at_unix_ms"}
	mock.ExpectQuery("SELECT MAX\\(n.id\\).*CURRENT_TIMESTAMP.*read_cutoff_unix_ms").WithArgs(int64(42), int64(200)).WillReturnRows(sqlmock.NewRows([]string{"upper_notification_id", "read_cutoff_unix_ms"}).AddRow(99, cutoff))
	mock.ExpectQuery("SELECT count\\(\\*\\)").WithArgs(int64(42), int64(200)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("ORDER BY .*1790873999999.* DESC,n.id DESC").WithArgs(int64(42), int64(200), int64(99), 2).WillReturnRows(sqlmock.NewRows(columns).AddRow(99, 200, 70, "A", 1, 8, 0, 1, cutoff-2000, 0).AddRow(98, 200, 71, "B", 1, 8, 0, 1, cutoff-1000, 0))
	first, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{TeamId: 200, Limit: 1})
	if err != nil || len(first.Notifications) != 1 || first.Notifications[0].NotificationId != 99 || first.NextCursor == "" {
		t.Fatalf("first=%v err=%v", first, err)
	}
	firstCursor, err := decodeNotificationCursor(first.NextCursor, 200)
	if err != nil || firstCursor.ReadCutoffUnixMs != cutoff || firstCursor.Phase != "unread" || firstCursor.LastID != 99 {
		t.Fatalf("cursor=%+v err=%v", firstCursor, err)
	}

	mock.ExpectQuery("SELECT count\\(\\*\\)").WithArgs(int64(42), int64(200)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("WHERE .*n.id < .*1790873999999.*ORDER BY .*1790873999999.* DESC,n.id DESC").WithArgs(int64(42), int64(200), int64(99), int64(99), 2).WillReturnRows(sqlmock.NewRows(columns).AddRow(98, 200, 71, "B", 1, 8, 0, 1, cutoff-1000, currentSecond).AddRow(97, 200, 72, "C", 2, 9, 1, 2, cutoff-3000, cutoff-999))
	second, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{TeamId: 200, Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Notifications) != 1 || second.Notifications[0].NotificationId != 98 || second.NextCursor == "" {
		t.Fatalf("second=%v err=%v", second, err)
	}
	secondCursor, _ := decodeNotificationCursor(second.NextCursor, 200)
	if secondCursor.Phase != "unread" || secondCursor.LastID != 98 || secondCursor.ReadCutoffUnixMs != cutoff {
		t.Fatalf("second cursor=%+v", secondCursor)
	}

	mock.ExpectQuery("SELECT count\\(\\*\\)").WithArgs(int64(42), int64(200)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("WHERE .*n.id < .*1790873999999.*ORDER BY .*1790873999999.* DESC,n.id DESC").WithArgs(int64(42), int64(200), int64(99), int64(98), 2).WillReturnRows(sqlmock.NewRows(columns).AddRow(97, 200, 72, "C", 2, 9, 1, 2, cutoff-3000, cutoff-999))
	third, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{TeamId: 200, Limit: 1, Cursor: second.NextCursor})
	if err != nil || len(third.Notifications) != 1 || third.Notifications[0].NotificationId != 97 || third.NextCursor != "" {
		t.Fatalf("third=%v err=%v", third, err)
	}
}

func TestListMyTaskNotificationsRejectsAccessChangeAfterQuery(t *testing.T) {
	s, mock := testTaskServer(t)
	s.identityClient = taskIdentityFake(func(context.Context) (*userpb.GetUserInfoResponse, error) {
		return &userpb.GetUserInfoResponse{Id: 42}, nil
	})
	s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		return &userpb.ListMyTeamsResponse{}, nil
	})
	checks := 0
	s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
		checks++
		if checks == 2 {
			return nil, status.Error(codes.PermissionDenied, "left")
		}
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	}}
	mock.ExpectQuery("SELECT MAX\\(n.id\\).*read_cutoff_unix_ms").WithArgs(int64(42), int64(200)).WillReturnRows(sqlmock.NewRows([]string{"upper_notification_id", "read_cutoff_unix_ms"}).AddRow(nil, 1790873999999))
	result, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{TeamId: 200})
	if result != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func TestListMyTaskNotificationsPagesTeamDirectoryAndEmptyResult(t *testing.T) {
	s, mock := testTaskServer(t)
	identities, pages := 0, 0
	s.identityClient = taskIdentityFake(func(context.Context) (*userpb.GetUserInfoResponse, error) {
		identities++
		return &userpb.GetUserInfoResponse{Id: 42}, nil
	})
	s.teamDirectory = taskDirectoryFake(func(_ context.Context, req *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		pages++
		if req.AfterTeamId == 0 {
			return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 200}}, NextAfterTeamId: 200}, nil
		}
		return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 300}}}, nil
	})
	mock.ExpectQuery("SELECT MAX\\(n.id\\).*read_cutoff_unix_ms").WithArgs(int64(42), int64(200), int64(300)).WillReturnRows(sqlmock.NewRows([]string{"upper_notification_id", "read_cutoff_unix_ms"}).AddRow(nil, 1790873999999))
	result, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{})
	if err != nil || len(result.Notifications) != 0 || result.UnreadCount != 0 || identities != 2 || pages != 4 {
		t.Fatalf("result=%v err=%v identities=%d pages=%d", result, err, identities, pages)
	}
}

func TestListMyTaskNotificationsReturnsEmptyWithoutDatabaseForNoTeams(t *testing.T) {
	s, _ := testTaskServer(t)
	identities, directories := 0, 0
	s.identityClient = taskIdentityFake(func(context.Context) (*userpb.GetUserInfoResponse, error) {
		identities++
		return &userpb.GetUserInfoResponse{Id: 42}, nil
	})
	s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		directories++
		return &userpb.ListMyTeamsResponse{}, nil
	})
	result, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{})
	if err != nil || result == nil || len(result.Notifications) != 0 || result.NextCursor != "" || result.UnreadCount != 0 || identities != 2 || directories != 2 {
		t.Fatalf("result=%v err=%v identity=%d directory=%d", result, err, identities, directories)
	}
}

func TestListMyTaskNotificationsDoesNotHideDatabaseUserOrCancellationErrors(t *testing.T) {
	t.Run("database", func(t *testing.T) {
		s, mock := testPersonalServer(t)
		mock.ExpectQuery("SELECT MAX\\(n.id\\)").WillReturnError(errors.New("db down"))
		result, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{})
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("result=%v err=%v", result, err)
		}
	})
	t.Run("user", func(t *testing.T) {
		s, _ := testTaskServer(t)
		s.identityClient = taskIdentityFake(func(context.Context) (*userpb.GetUserInfoResponse, error) {
			return nil, status.Error(codes.DeadlineExceeded, "user")
		})
		s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
			return nil, nil
		})
		result, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{})
		if result != nil || status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("result=%v err=%v", result, err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		s, _ := testTaskServer(t)
		s.identityClient = taskIdentityFake(func(ctx context.Context) (*userpb.GetUserInfoResponse, error) { return nil, ctx.Err() })
		s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
			return nil, nil
		})
		ctx, cancel := context.WithCancel(taskListContext())
		cancel()
		result, err := s.ListMyTaskNotifications(ctx, &pb.ListMyTaskNotificationsRequest{})
		if result != nil || status.Code(err) != codes.Canceled {
			t.Fatalf("result=%v err=%v", result, err)
		}
	})
}

func TestListMyTaskNotificationsRejectsIdentityChangeAndInvalidRows(t *testing.T) {
	t.Run("identity changed", func(t *testing.T) {
		s, mock := testTaskServer(t)
		calls := 0
		s.identityClient = taskIdentityFake(func(context.Context) (*userpb.GetUserInfoResponse, error) {
			calls++
			return &userpb.GetUserInfoResponse{Id: int64(41 + calls)}, nil
		})
		s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
			return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 200}}}, nil
		})
		mock.ExpectQuery("SELECT MAX\\(n.id\\).*read_cutoff_unix_ms").WithArgs(int64(42), int64(200)).WillReturnRows(sqlmock.NewRows([]string{"upper_notification_id", "read_cutoff_unix_ms"}).AddRow(nil, 1790873999999))
		result, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{})
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("result=%v err=%v", result, err)
		}
	})
	t.Run("invalid joined row", func(t *testing.T) {
		s, mock := testPersonalServer(t)
		mock.ExpectQuery("SELECT MAX\\(n.id\\).*read_cutoff_unix_ms").WillReturnRows(sqlmock.NewRows([]string{"upper_notification_id", "read_cutoff_unix_ms"}).AddRow(9, 1790873999999))
		mock.ExpectQuery("SELECT count\\(\\*\\)").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
		mock.ExpectQuery("SELECT n.id AS notification_id").WillReturnRows(sqlmock.NewRows([]string{"notification_id", "team_id", "task_id", "task_title", "current_status", "actor_id", "from_status", "to_status", "created_at_unix_ms", "read_at_unix_ms"}).AddRow(9, 999, 7, "leak", 1, 8, 0, 1, 1790873999999, 0))
		result, err := s.ListMyTaskNotifications(taskListContext(), &pb.ListMyTaskNotificationsRequest{})
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("result=%v err=%v", result, err)
		}
	})
}

func TestNotificationCursorBindsTeamAndRejectsMalformed(t *testing.T) {
	if _, err := decodeNotificationCursor("not-json", 0); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	encoded := encodeNotificationCursor(notificationCursor{Version: 1, TeamID: 7, UpperID: 10, ReadCutoffUnixMs: 1790873999999, Phase: "unread", LastID: 9})
	if _, err := decodeNotificationCursor(encoded, 8); err == nil {
		t.Fatal("cross-team cursor accepted")
	}
	withoutSnapshot := encodeNotificationCursor(notificationCursor{Version: 1, TeamID: 7, UpperID: 10, Phase: "unread", LastID: 9})
	if _, err := decodeNotificationCursor(withoutSnapshot, 7); err == nil {
		t.Fatal("cursor without snapshot accepted")
	}
	if _, err := decodeNotificationCursor(encoded+"A", 7); err == nil {
		t.Fatal("non-canonical cursor accepted")
	}
}

func TestSnapshotUnreadClassificationSurvivesReadTransition(t *testing.T) {
	cursor := notificationCursor{Version: 1, TeamID: 0, UpperID: 99, ReadCutoffUnixMs: 1790873999999, Phase: "unread", LastID: 99}
	if !notificationWasUnreadAt(0, cursor.ReadCutoffUnixMs) || !notificationWasUnreadAt(1790874000000, cursor.ReadCutoffUnixMs) || notificationWasUnreadAt(1790873999000, cursor.ReadCutoffUnixMs) {
		t.Fatal("snapshot partition changed after read")
	}
}
