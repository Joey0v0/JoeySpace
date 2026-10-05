package main

import (
	"context"
	"errors"
	"math"
	"net"
	"regexp"
	"strconv"
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

const notificationReadSelect = "SELECT id AS notification_id, CAST(UNIX_TIMESTAMP(created_at) * 1000 AS SIGNED) AS created_at_unix_ms, COALESCE(CAST(UNIX_TIMESTAMP(read_at) * 1000 AS SIGNED), 0) AS read_at_unix_ms FROM `task_status_notifications` WHERE id = ? AND team_id = ? AND recipient_id = ? LIMIT ?"
const notificationReadLock = notificationReadSelect + " FOR UPDATE"
const notificationReadUpdate = "UPDATE `task_status_notifications` SET `read_at`=CURRENT_TIMESTAMP WHERE id = ? AND team_id = ? AND recipient_id = ? AND read_at IS NULL"
const noticeCreated = int64(1790874000000)
const noticeRead = int64(1790874001000)

func readNotificationRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"notification_id", "created_at_unix_ms", "read_at_unix_ms"})
}

func notificationReadRequest() *pb.MarkTaskNotificationReadRequest {
	return &pb.MarkTaskNotificationReadRequest{TeamId: 200, NotificationId: 9}
}

func expectUnreadNotification(mock sqlmock.Sqlmock, id int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(notificationReadLock)).WithArgs(id, int64(200), int64(42), 1).
		WillReturnRows(readNotificationRows().AddRow(id, noticeCreated, 0))
}

func expectNotificationReadWrite(mock sqlmock.Sqlmock, id int64, commitError error) {
	mock.ExpectExec(regexp.QuoteMeta(notificationReadUpdate)).WithArgs(id, int64(200), int64(42)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(notificationReadSelect)).WithArgs(id, int64(200), int64(42), 1).
		WillReturnRows(readNotificationRows().AddRow(id, noticeCreated, noticeRead))
	commit := mock.ExpectCommit()
	if commitError != nil {
		commit.WillReturnError(commitError)
	}
}

func expectAlreadyReadNotification(mock sqlmock.Sqlmock, id int64) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(notificationReadLock)).WithArgs(id, int64(200), int64(42), 1).
		WillReturnRows(readNotificationRows().AddRow(id, noticeCreated, noticeRead))
	mock.ExpectCommit()
}

func TestMarkTaskNotificationReadFixesFirstTimeAndOriginalRecipient(t *testing.T) {
	s, mock := testTaskServer(t)
	checks := 0
	s.teamClient = teamCheckerFake{checkMember: func(ctx context.Context, teamID int64) (*userpb.CheckTeamMemberResponse, error) {
		checks++
		md, _ := metadata.FromOutgoingContext(ctx)
		if teamID != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer sample" || len(md.Get("recipient-id")) != 0 {
			t.Errorf("unexpected authorization scope: team=%d metadata=%v", teamID, md)
		}
		if checks == 2 {
			// There must be no open SQL work at the final User RPC.
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Errorf("final membership RPC happened before SQL commit: %v", err)
			}
		}
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	}}
	expectUnreadNotification(mock, math.MaxInt64)
	expectNotificationReadWrite(mock, math.MaxInt64, nil)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer sample", "recipient-id", "77"))
	result, err := s.MarkTaskNotificationRead(ctx, &pb.MarkTaskNotificationReadRequest{TeamId: 200, NotificationId: math.MaxInt64})
	if err != nil || result == nil || result.NotificationId != math.MaxInt64 || result.ReadAtUnixMs != noticeRead || checks != 2 {
		t.Fatalf("acknowledgement=%v err=%v checks=%d", result, err, checks)
	}
	// Exact SQL expectations above admit only one personal row update, not another recipient or notice.
}

func TestMarkTaskNotificationReadReplayAfterLostResponseAndServerRebuild(t *testing.T) {
	s, mock := testTaskServer(t)
	expectUnreadNotification(mock, 9)
	expectNotificationReadWrite(mock, 9, nil)
	if result, err := s.MarkTaskNotificationRead(taskListContext(), notificationReadRequest()); err != nil || result.ReadAtUnixMs != noticeRead {
		t.Fatalf("initial acknowledgement=%v error=%v", result, err)
	}
	// Discard the successful response and reconstruct the production handler around the persistent store.
	rebuilt := &taskServer{db: s.db, teamClient: s.teamClient}
	expectAlreadyReadNotification(mock, 9)
	result, err := rebuilt.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
	if err != nil || result == nil || result.NotificationId != 9 || result.ReadAtUnixMs != noticeRead {
		t.Fatalf("replay=%v error=%v", result, err)
	}
	// No UPDATE or second read-back is expected on replay; the first database time survives.
}

func TestMarkTaskNotificationReadUncertainCommitReturnsFailureAndReplayConverges(t *testing.T) {
	s, mock := testTaskServer(t)
	expectUnreadNotification(mock, 9)
	expectNotificationReadWrite(mock, 9, errors.New("private commit acknowledgement lost"))
	result, err := s.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
	if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
		t.Fatalf("uncertain commit=%v error=%v", result, err)
	}
	// Model the branch where MySQL committed but its acknowledgement was lost.
	expectAlreadyReadNotification(mock, 9)
	rebuilt := &taskServer{db: s.db, teamClient: s.teamClient}
	result, err = rebuilt.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
	if err != nil || result == nil || result.ReadAtUnixMs != noticeRead {
		t.Fatalf("uncertain replay=%v error=%v", result, err)
	}
}

func TestMarkTaskNotificationReadAbsentAndOtherRecipientsHaveSameNotFound(t *testing.T) {
	for _, noticeID := range []int64{9, 10, math.MaxInt64} {
		t.Run(strconv.FormatInt(noticeID, 10), func(t *testing.T) {
			s, mock := testTaskServer(t)
			mock.ExpectBegin()
			mock.ExpectQuery(regexp.QuoteMeta(notificationReadLock)).WithArgs(noticeID, int64(200), int64(42), 1).
				WillReturnRows(readNotificationRows())
			mock.ExpectRollback()
			result, err := s.MarkTaskNotificationRead(taskListContext(), &pb.MarkTaskNotificationReadRequest{TeamId: 200, NotificationId: noticeID})
			if result != nil || status.Code(err) != codes.NotFound || status.Convert(err).Message() != "task notification not found" {
				t.Fatalf("not-found=%v error=%v", result, err)
			}
		})
	}
}

func TestMarkTaskNotificationReadRejectsInvalidParametersAndMissingLogin(t *testing.T) {
	s, _ := testTaskServer(t)
	for _, req := range []*pb.MarkTaskNotificationReadRequest{nil, {}, {TeamId: 200}, {NotificationId: 9}, {TeamId: -1, NotificationId: 9}, {TeamId: 200, NotificationId: -1}} {
		result, err := s.MarkTaskNotificationRead(taskListContext(), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("request=%v result=%v error=%v", req, result, err)
		}
	}
	for _, ctx := range []context.Context{context.Background(), metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "")),
		metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer one", "authorization", "Bearer two"))} {
		result, err := s.MarkTaskNotificationRead(ctx, notificationReadRequest())
		if result != nil || status.Code(err) != codes.Unauthenticated {
			t.Fatalf("login=%v error=%v", result, err)
		}
	}
	for _, disabled := range []*taskServer{{db: s.db}, {teamClient: s.teamClient}} {
		result, err := disabled.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("disabled=%v error=%v", result, err)
		}
	}
}

func TestMarkTaskNotificationReadInitialMembershipFailurePreventsSQL(t *testing.T) {
	for _, code := range []codes.Code{codes.Unauthenticated, codes.PermissionDenied, codes.NotFound, codes.FailedPrecondition, codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Internal} {
		t.Run(code.String(), func(t *testing.T) {
			s, _ := testTaskServer(t)
			s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
				return nil, status.Error(code, "membership unavailable")
			}}
			result, err := s.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
			want := code
			if code == codes.Internal {
				want = codes.Unavailable
			}
			if result != nil || status.Code(err) != want {
				t.Fatalf("membership=%v error=%v", result, err)
			}
		})
	}
	for _, member := range []*userpb.CheckTeamMemberResponse{nil, {}, {UserId: -1}} {
		s, _ := testTaskServer(t)
		s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) { return member, nil }}
		result, err := s.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("invalid member=%v error=%v", result, err)
		}
	}
}

func TestMarkTaskNotificationReadFinalMembershipFailureDoesNotUndoCommittedTime(t *testing.T) {
	for _, outcome := range []string{"left team", "identity changed", "membership unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			s, mock := testTaskServer(t)
			checks := 0
			s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
				checks++
				if checks == 2 {
					switch outcome {
					case "left team":
						return nil, status.Error(codes.PermissionDenied, "team membership required")
					case "identity changed":
						return &userpb.CheckTeamMemberResponse{UserId: 77}, nil
					default:
						return nil, status.Error(codes.Unavailable, "membership unavailable")
					}
				}
				return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
			}}
			expectUnreadNotification(mock, 9)
			expectNotificationReadWrite(mock, 9, nil)
			result, err := s.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
			want := codes.Unavailable
			if outcome == "left team" {
				want = codes.PermissionDenied
			}
			if result != nil || status.Code(err) != want || checks != 2 {
				t.Fatalf("post-commit denial=%v error=%v checks=%d", result, err, checks)
			}
			// After current eligibility returns, retry reads the committed time without rewriting it.
			expectAlreadyReadNotification(mock, 9)
			result, err = s.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
			if err != nil || result == nil || result.ReadAtUnixMs != noticeRead {
				t.Fatalf("eligible retry=%v error=%v", result, err)
			}
		})
	}
}

func TestMarkTaskNotificationReadTransactionFailuresRollbackAndDoNotClaimRead(t *testing.T) {
	for _, failure := range []string{"begin", "migration missing", "update", "zero affected", "two affected", "read-back", "invalid saved time"} {
		t.Run(failure, func(t *testing.T) {
			s, mock := testTaskServer(t)
			if failure == "begin" {
				mock.ExpectBegin().WillReturnError(errors.New("private begin failure"))
			} else if failure == "migration missing" {
				mock.ExpectBegin()
				mock.ExpectQuery(regexp.QuoteMeta(notificationReadLock)).WithArgs(int64(9), int64(200), int64(42), 1).
					WillReturnError(errors.New("private Unknown column read_at"))
				mock.ExpectRollback()
			} else {
				expectUnreadNotification(mock, 9)
				write := mock.ExpectExec(regexp.QuoteMeta(notificationReadUpdate)).WithArgs(int64(9), int64(200), int64(42))
				switch failure {
				case "update":
					write.WillReturnError(errors.New("private update failure"))
				case "zero affected":
					write.WillReturnResult(sqlmock.NewResult(0, 0))
				case "two affected":
					write.WillReturnResult(sqlmock.NewResult(0, 2))
				default:
					write.WillReturnResult(sqlmock.NewResult(0, 1))
					read := mock.ExpectQuery(regexp.QuoteMeta(notificationReadSelect)).WithArgs(int64(9), int64(200), int64(42), 1)
					if failure == "read-back" {
						read.WillReturnError(errors.New("private read-back failure"))
					} else {
						read.WillReturnRows(readNotificationRows().AddRow(9, noticeCreated, noticeCreated-1))
					}
				}
				mock.ExpectRollback()
			}
			result, err := s.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
			if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
				t.Fatalf("transaction failure=%v error=%v", result, err)
			}
		})
	}
}

func TestMarkTaskNotificationReadRejectsCorruptLockedRowsWithoutWrite(t *testing.T) {
	for _, row := range [][]any{
		{10, noticeCreated, 0}, {9, int64(0), 0}, {9, maxTaskDueAtUnixMs + 1, 0},
		{9, noticeCreated, -1}, {9, noticeCreated, noticeCreated - 1}, {9, noticeCreated, maxTaskDueAtUnixMs + 1},
	} {
		s, mock := testTaskServer(t)
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta(notificationReadLock)).WithArgs(int64(9), int64(200), int64(42), 1).
			WillReturnRows(readNotificationRows().AddRow(row[0], row[1], row[2]))
		mock.ExpectRollback()
		result, err := s.MarkTaskNotificationRead(taskListContext(), notificationReadRequest())
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("corrupt record=%v result=%v error=%v", row, result, err)
		}
	}
}

func TestMarkTaskNotificationReadCancellationAndDeadlineDoNotWrite(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		s, mock := testTaskServer(t)
		var ctx context.Context
		var cancel context.CancelFunc
		if deadline {
			ctx, cancel = context.WithTimeout(taskListContext(), 20*time.Millisecond)
		} else {
			ctx, cancel = context.WithCancel(taskListContext())
			time.AfterFunc(20*time.Millisecond, cancel)
		}
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta(notificationReadLock)).WithArgs(int64(9), int64(200), int64(42), 1).
			WillDelayFor(time.Second).WillReturnRows(readNotificationRows().AddRow(9, noticeCreated, 0))
		mock.ExpectRollback()
		result, err := s.MarkTaskNotificationRead(ctx, notificationReadRequest())
		cancel()
		want := codes.Canceled
		if deadline {
			want = codes.DeadlineExceeded
		}
		if result != nil || status.Code(err) != want {
			t.Fatalf("canceled SQL=%v error=%v", result, err)
		}
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
		result, err := s.MarkTaskNotificationRead(ctx, notificationReadRequest())
		cancel()
		if result != nil || status.Code(err) != codes.Canceled {
			t.Fatalf("canceled before SQL=%v error=%v", result, err)
		}
	}
}

func TestMarkTaskNotificationReadOverTCPRPCAndReplay(t *testing.T) {
	s, mock := testTaskServer(t)
	expectUnreadNotification(mock, math.MaxInt64)
	expectNotificationReadWrite(mock, math.MaxInt64, nil)
	expectAlreadyReadNotification(mock, math.MaxInt64)
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
	client := pb.NewTaskClient(conn)
	for attempt := 0; attempt < 2; attempt++ {
		result, err := client.MarkTaskNotificationRead(ctx, &pb.MarkTaskNotificationReadRequest{TeamId: 200, NotificationId: math.MaxInt64})
		if err != nil || result == nil || result.NotificationId != math.MaxInt64 || result.ReadAtUnixMs != noticeRead {
			t.Fatalf("TCP acknowledgement attempt %d=%v error=%v", attempt, result, err)
		}
	}
}
