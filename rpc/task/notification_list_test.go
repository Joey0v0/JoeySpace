package main

import (
	"context"
	"errors"
	"math"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const notificationListSelect = "SELECT n.id AS notification_id, n.task_id, o.actor_id, o.from_status, o.to_status, CAST(UNIX_TIMESTAMP(n.created_at) * 1000 AS SIGNED) AS created_at_unix_ms FROM task_status_notifications AS n JOIN task_operations AS o ON o.id = n.operation_id AND o.task_id = n.task_id WHERE "
const notificationListQuery = notificationListSelect + "n.team_id = ? AND n.recipient_id = ? ORDER BY n.id DESC LIMIT ?"
const notificationCursorQuery = notificationListSelect + "(n.team_id = ? AND n.recipient_id = ?) AND n.id < ? ORDER BY n.id DESC LIMIT ?"

func notificationRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"notification_id", "task_id", "actor_id", "from_status", "to_status", "created_at_unix_ms"})
}

func TestListTaskNotificationsPersonalIsolationAndLargeCursor(t *testing.T) {
	s, mock := testTaskServer(t)
	checks := 0
	s.teamClient = teamCheckerFake{checkMember: func(ctx context.Context, teamID int64) (*userpb.CheckTeamMemberResponse, error) {
		checks++
		md, _ := metadata.FromOutgoingContext(ctx)
		if teamID != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer sample" || len(md.Get("recipient-id")) != 0 {
			t.Errorf("membership request: team=%d metadata=%v", teamID, md)
		}
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	}}
	mock.ExpectQuery(regexp.QuoteMeta(notificationCursorQuery)).WithArgs(int64(200), int64(42), int64(math.MaxInt64), 3).
		WillReturnRows(notificationRows().
			AddRow(math.MaxInt64-1, math.MaxInt64-10, int64(77), 0, 2, int64(1790874000123)).
			AddRow(math.MaxInt64-2, int64(51), int64(43), 2, 1, int64(1790874000000)).
			AddRow(math.MaxInt64-3, int64(52), int64(44), 1, 0, int64(1790873999999)))
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer sample", "recipient-id", "99"))
	result, err := s.ListTaskNotifications(ctx, &pb.ListTaskNotificationsRequest{TeamId: 200, BeforeNotificationId: math.MaxInt64, Limit: 2})
	if err != nil || result == nil || len(result.Notifications) != 2 || result.NextBeforeNotificationId != math.MaxInt64-2 || checks != 2 {
		t.Fatalf("page=%v error=%v membership checks=%d", result, err, checks)
	}
	first := result.Notifications[0]
	if first.NotificationId != math.MaxInt64-1 || first.TaskId != math.MaxInt64-10 || first.ActorId != 77 || first.FromStatus != 0 || first.ToStatus != 2 || first.CreatedAtUnixMs != 1790874000123 {
		t.Fatalf("first notification=%v", first)
	}
}

func TestListTaskNotificationsOverTCPRPC(t *testing.T) {
	s, mock := testTaskServer(t)
	checks := 0
	s.teamClient = teamCheckerFake{checkMember: func(ctx context.Context, teamID int64) (*userpb.CheckTeamMemberResponse, error) {
		checks++
		md, _ := metadata.FromOutgoingContext(ctx)
		if teamID != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer sample" {
			t.Errorf("wrong RPC membership request: team=%d metadata=%v", teamID, md)
		}
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	}}
	mock.ExpectQuery(regexp.QuoteMeta(notificationListQuery)).WithArgs(int64(200), int64(42), 2).
		WillReturnRows(notificationRows().AddRow(math.MaxInt64-1, math.MaxInt64-10, 77, 2, 0, 1790874000123))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterTaskServer(server, s)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer sample"))
	result, err := pb.NewTaskClient(conn).ListTaskNotifications(ctx, &pb.ListTaskNotificationsRequest{TeamId: 200, Limit: 1})
	if err != nil || result == nil || len(result.Notifications) != 1 || result.Notifications[0].NotificationId != math.MaxInt64-1 || result.Notifications[0].TaskId != math.MaxInt64-10 || result.Notifications[0].ActorId != 77 || result.Notifications[0].FromStatus != 2 || result.Notifications[0].ToStatus != 0 || result.Notifications[0].CreatedAtUnixMs != 1790874000123 || result.NextBeforeNotificationId != 0 || checks != 2 {
		t.Fatalf("RPC notification page=%v error=%v checks=%d", result, err, checks)
	}
}

func TestListTaskNotificationsDefaultsEmptyAndFinalPage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int32
		rows  *sqlmock.Rows
		count int
	}{
		{"default empty", 0, notificationRows(), 0},
		{"maximum empty", 100, notificationRows(), 0},
		{"exact full final page", 1, notificationRows().AddRow(9, 51, 43, 1, 2, 1790874000000), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testTaskServer(t)
			limit := int(tc.limit)
			if limit == 0 {
				limit = 20
			}
			mock.ExpectQuery(regexp.QuoteMeta(notificationListQuery)).WithArgs(int64(200), int64(42), limit+1).WillReturnRows(tc.rows)
			result, err := s.ListTaskNotifications(taskListContext(), &pb.ListTaskNotificationsRequest{TeamId: 200, Limit: tc.limit})
			if err != nil || result == nil || result.Notifications == nil || len(result.Notifications) != tc.count || result.NextBeforeNotificationId != 0 {
				t.Fatalf("final page=%v error=%v", result, err)
			}
		})
	}
}

func TestListTaskNotificationsRejectsInvalidParameters(t *testing.T) {
	s, _ := testTaskServer(t)
	for _, req := range []*pb.ListTaskNotificationsRequest{nil, {}, {TeamId: -1}, {TeamId: 200, BeforeNotificationId: -1}, {TeamId: 200, Limit: -1}, {TeamId: 200, Limit: 101}} {
		result, err := s.ListTaskNotifications(taskListContext(), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("request=%v result=%v error=%v", req, result, err)
		}
	}
}

func TestListTaskNotificationsRejectsMissingLoginAndUnavailableService(t *testing.T) {
	s, _ := testTaskServer(t)
	for _, ctx := range []context.Context{context.Background(), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "")), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer one", "authorization", "Bearer two"))} {
		result, err := s.ListTaskNotifications(ctx, &pb.ListTaskNotificationsRequest{TeamId: 200})
		if result != nil || status.Code(err) != codes.Unauthenticated {
			t.Fatalf("login: %v, %v", result, err)
		}
	}
	for _, service := range []*taskServer{{teamClient: s.teamClient}, {db: s.db}} {
		result, err := service.ListTaskNotifications(taskListContext(), &pb.ListTaskNotificationsRequest{TeamId: 200})
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("disabled: %v, %v", result, err)
		}
	}
}

func TestListTaskNotificationsCurrentTeamFailure(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.FailedPrecondition, codes.Unauthenticated, codes.NotFound, codes.DeadlineExceeded, codes.Canceled, codes.Internal} {
		t.Run(code.String(), func(t *testing.T) {
			s, _ := testTaskServer(t)
			s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
				return nil, status.Error(code, "private membership detail")
			}}
			result, err := s.ListTaskNotifications(taskListContext(), &pb.ListTaskNotificationsRequest{TeamId: 200})
			want := code
			if code == codes.Internal {
				want = codes.Unavailable
			}
			if result != nil || status.Code(err) != want {
				t.Fatalf("membership: %v, %v", result, err)
			}
		})
	}
	for _, member := range []*userpb.CheckTeamMemberResponse{nil, {}, {UserId: -1}} {
		s, _ := testTaskServer(t)
		s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) { return member, nil }}
		result, err := s.ListTaskNotifications(taskListContext(), &pb.ListTaskNotificationsRequest{TeamId: 200})
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("invalid membership: %v, %v", result, err)
		}
	}
}

func TestListTaskNotificationsRechecksMembershipBeforeReturn(t *testing.T) {
	for _, populated := range []bool{false, true} {
		for _, changedIdentity := range []bool{false, true} {
			s, mock := testTaskServer(t)
			checks := 0
			s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
				checks++
				if checks == 1 {
					return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
				}
				if changedIdentity {
					return &userpb.CheckTeamMemberResponse{UserId: 77}, nil
				}
				return nil, status.Error(codes.PermissionDenied, "left team during query")
			}}
			rows := notificationRows()
			if populated {
				rows.AddRow(9, 51, 43, 1, 2, 1790874000000)
			}
			mock.ExpectQuery(regexp.QuoteMeta(notificationListQuery)).WithArgs(int64(200), int64(42), 21).WillReturnRows(rows)
			result, err := s.ListTaskNotifications(taskListContext(), &pb.ListTaskNotificationsRequest{TeamId: 200})
			want := codes.PermissionDenied
			if changedIdentity {
				want = codes.Unavailable
			}
			if result != nil || status.Code(err) != want || checks != 2 {
				t.Fatalf("revoked: %v, %v, checks=%d", result, err, checks)
			}
		}
	}
}

func TestListTaskNotificationsDatabaseFailureAndCancellation(t *testing.T) {
	t.Run("private database error", func(t *testing.T) {
		s, mock := testTaskServer(t)
		mock.ExpectQuery(regexp.QuoteMeta(notificationListQuery)).WithArgs(int64(200), int64(42), 21).WillReturnError(errors.New("private DB detail"))
		result, err := s.ListTaskNotifications(taskListContext(), &pb.ListTaskNotificationsRequest{TeamId: 200})
		if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(status.Convert(err).Message(), "private DB detail") {
			t.Fatalf("DB failure: %v, %v", result, err)
		}
	})
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled query", true: "query deadline"}[deadline], func(t *testing.T) {
			s, mock := testTaskServer(t)
			var ctx context.Context
			var cancel context.CancelFunc
			if deadline {
				ctx, cancel = context.WithTimeout(taskListContext(), 20*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(taskListContext())
				time.AfterFunc(20*time.Millisecond, cancel)
			}
			defer cancel()
			mock.ExpectQuery(regexp.QuoteMeta(notificationListQuery)).WithArgs(int64(200), int64(42), 21).WillDelayFor(time.Second).WillReturnRows(notificationRows())
			result, err := s.ListTaskNotifications(ctx, &pb.ListTaskNotificationsRequest{TeamId: 200})
			want := codes.Canceled
			if deadline {
				want = codes.DeadlineExceeded
			}
			if result != nil || status.Code(err) != want {
				t.Fatalf("context failure: %v, %v", result, err)
			}
		})
	}
	for _, duringMembership := range []bool{false, true} {
		s, _ := testTaskServer(t)
		ctx, cancel := context.WithCancel(taskListContext())
		if duringMembership {
			s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
				cancel()
				return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
			}}
		} else {
			cancel()
		}
		result, err := s.ListTaskNotifications(ctx, &pb.ListTaskNotificationsRequest{TeamId: 200})
		cancel()
		if result != nil || status.Code(err) != codes.Canceled {
			t.Fatalf("canceled before SQL: %v, %v", result, err)
		}
	}
}

func TestListTaskNotificationsRejectsInvalidDatabaseResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
	}{
		{"zero notification", notificationRows().AddRow(0, 51, 43, 0, 1, 1790874000000)},
		{"zero task", notificationRows().AddRow(9, 0, 43, 0, 1, 1790874000000)},
		{"zero actor", notificationRows().AddRow(9, 51, 0, 0, 1, 1790874000000)},
		{"invalid source status", notificationRows().AddRow(9, 51, 43, -1, 1, 1790874000000)},
		{"invalid target status", notificationRows().AddRow(9, 51, 43, 0, 3, 1790874000000)},
		{"same status", notificationRows().AddRow(9, 51, 43, 1, 1, 1790874000000)},
		{"null time", notificationRows().AddRow(9, 51, 43, 0, 1, nil)},
		{"time out of range", notificationRows().AddRow(9, 51, 43, 0, 1, maxTaskDueAtUnixMs+1)},
		{"outside cursor", notificationRows().AddRow(10, 51, 43, 0, 1, 1790874000000)},
		{"duplicate id", notificationRows().AddRow(9, 51, 43, 0, 1, 1790874000000).AddRow(9, 52, 44, 1, 2, 1790874000000)},
		{"ascending id", notificationRows().AddRow(8, 51, 43, 0, 1, 1790874000000).AddRow(9, 52, 44, 1, 2, 1790874000000)},
		{"invalid hidden next row", notificationRows().AddRow(9, 51, 43, 0, 1, 1790874000000).AddRow(8, 52, 0, 1, 2, 1790874000000)},
		{"too many rows", notificationRows().AddRow(9, 51, 43, 0, 1, 1790874000000).AddRow(8, 52, 44, 1, 2, 1790874000000).AddRow(7, 53, 45, 0, 2, 1790874000000)},
		{"scan failure", notificationRows().AddRow("not an ID", 51, 43, 0, 1, 1790874000000)},
		{"row stream failure", notificationRows().AddRow(9, 51, 43, 0, 1, 1790874000000).RowError(0, errors.New("private row detail"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testTaskServer(t)
			mock.ExpectQuery(regexp.QuoteMeta(notificationCursorQuery)).WithArgs(int64(200), int64(42), int64(10), 2).WillReturnRows(tc.rows)
			result, err := s.ListTaskNotifications(taskListContext(), &pb.ListTaskNotificationsRequest{TeamId: 200, BeforeNotificationId: 10, Limit: 1})
			if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(status.Convert(err).Message(), "private") {
				t.Fatalf("invalid rows: %v, %v", result, err)
			}
		})
	}
}
