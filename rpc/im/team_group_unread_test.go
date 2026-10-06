package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"math"
	"net"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// This exact query checks the business exclusions, reader/group scoping and
// absence of an ID/time watermark. SQL itself still uses a database substitute.
const unreadCountQuery = `SELECT COUNT(*) FROM messages
WHERE messages.to_id = ? AND messages.chat_type = 2
AND NOT (messages.sender_type IN (0, 1) AND messages.from_id = ?)
AND NOT EXISTS (SELECT 1 FROM im_group_message_reads AS r
WHERE r.user_id = ? AND r.group_id = ? AND r.message_id = messages.id)`

func unreadAccess(mock sqlmock.Sqlmock, userID int64) {
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), userID, 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
	mock.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
}

func unreadCount(mock sqlmock.Sqlmock, userID, count int64) {
	mock.ExpectQuery(regexp.QuoteMeta(unreadCountQuery)).WithArgs(int64(300), userID, userID, int64(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
}

func unreadGet(mock sqlmock.Sqlmock, userID, count int64) {
	unreadAccess(mock, userID)
	unreadCount(mock, userID, count)
	unreadAccess(mock, userID)
}

func unreadLock(mock sqlmock.Sqlmock, ids ...int64) *sqlmock.ExpectedQuery {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	query := fmt.Sprintf("SELECT id, from_id, sender_type FROM `messages` WHERE to_id = ? AND chat_type = ? AND id IN (%s) ORDER BY id ASC FOR SHARE", placeholders)
	args := []driver.Value{int64(300), 2}
	for _, id := range ids {
		args = append(args, id)
	}
	return mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(args...)
}

func unreadRows() *sqlmock.Rows { return sqlmock.NewRows([]string{"id", "from_id", "sender_type"}) }

func unreadInsert(mock sqlmock.Sqlmock, userID int64, ids ...int64) *sqlmock.ExpectedExec {
	values := strings.TrimSuffix(strings.Repeat("(?,?,?),", len(ids)), ",")
	// No read_at argument or update is allowed: MySQL assigns the first value,
	// and GORM's MySQL DoNothing implementation self-assigns the primary key.
	query := "INSERT INTO `im_group_message_reads` (`user_id`,`group_id`,`message_id`) VALUES " + values + " ON DUPLICATE KEY UPDATE `user_id`=`user_id`"
	args := make([]driver.Value, 0, 3*len(ids))
	for _, id := range ids {
		args = append(args, userID, int64(300), id)
	}
	return mock.ExpectExec(regexp.QuoteMeta(query)).WithArgs(args...)
}

func unreadServer(t *testing.T) (*imServer, sqlmock.Sqlmock) {
	t.Helper()
	s, mock := testIMServer(t)
	s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error { return nil })
	return s, mock
}

func unreadRequest(ids ...int64) *pb.MarkTeamGroupMessagesReadRequest {
	return &pb.MarkTeamGroupMessagesReadRequest{TeamId: 200, GroupId: 300, MessageIds: ids}
}

func TestTeamGroupUnreadTCPReadExplicitReceiptsReplayAndLateSmallID(t *testing.T) {
	s, mock := unreadServer(t)
	authorization := "Bearer " + validIMToken(t)
	checks := 0
	s.teamClient = teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || !reflect.DeepEqual(md, metadata.Pairs("authorization", authorization)) {
			t.Errorf("qualification check lost original caller/scope: %v %v", req, md)
			return status.Error(codes.Unauthenticated, "invalid forwarded caller")
		}
		checks++
		return nil
	})
	unreadGet(mock, 42, 3)
	for attempt := 0; attempt < 2; attempt++ {
		unreadAccess(mock, 42)
		mock.ExpectBegin()
		unreadLock(mock, 100, 101, 102, math.MaxInt64).WillReturnRows(unreadRows().
			AddRow(100, 42, 0).AddRow(101, 42, 1).AddRow(102, 42, 2).AddRow(math.MaxInt64, 7, 1))
		unreadInsert(mock, 42, 102, math.MaxInt64).WillReturnResult(sqlmock.NewResult(0, int64(2-2*attempt)))
		mock.ExpectCommit()
		unreadGet(mock, 42, 1)
	}
	// A newly committed ID=1 must still contribute after confirming MaxInt64.
	// The same unrestricted count query is expected; no cursor can hide it.
	unreadGet(mock, 42, 2)
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", authorization, "user_id", "999"))
	client := pb.NewIMClient(conn)
	result, err := client.GetTeamGroupUnread(ctx, &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300})
	if err != nil || result.GetTeamId() != 200 || result.GetGroupId() != 300 || result.GetUnreadCount() != 3 {
		t.Fatalf("initial unread=%v error=%v", result, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		request := unreadRequest(math.MaxInt64, 101, 102, 100, 102)
		before := append([]int64(nil), request.MessageIds...)
		marked, err := client.MarkTeamGroupMessagesRead(ctx, request)
		if err != nil || marked.GetTeamId() != 200 || marked.GetGroupId() != 300 || marked.GetUnreadCount() != 1 || !reflect.DeepEqual(marked.GetMessageIds(), []int64{100, 101, 102, math.MaxInt64}) || !reflect.DeepEqual(request.MessageIds, before) {
			t.Fatalf("explicit/replayed read %d=%v error=%v", attempt, marked, err)
		}
	}
	result, err = client.GetTeamGroupUnread(ctx, &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300})
	if err != nil || result.GetUnreadCount() != 2 || checks != 10 {
		t.Fatalf("late small ID unread=%v error=%v checks=%d", result, err, checks)
	}
}

func TestTeamGroupUnreadAndReceiptsUseOnlyTokenReader(t *testing.T) {
	s, mock := unreadServer(t)
	token, err := pkgjwt.GenerateToken(43, imTestSecret, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token, "user_id", "42"))
	unreadGet(mock, 43, 9007199254740993)
	result, err := s.GetTeamGroupUnread(ctx, &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300})
	if err != nil || result.GetUnreadCount() != 9007199254740993 {
		t.Fatalf("reader43 count=%v error=%v", result, err)
	}
	unreadAccess(mock, 43)
	mock.ExpectBegin()
	unreadLock(mock, 9, 10).WillReturnRows(unreadRows().AddRow(9, 42, 1).AddRow(10, 43, 2))
	unreadInsert(mock, 43, 9, 10).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	unreadGet(mock, 43, 0)
	marked, err := s.MarkTeamGroupMessagesRead(ctx, unreadRequest(10, 9))
	if err != nil || marked.GetUnreadCount() != 0 || !reflect.DeepEqual(marked.GetMessageIds(), []int64{9, 10}) {
		t.Fatalf("reader43 mark=%v error=%v", marked, err)
	}
}

func TestTeamGroupReadOwnOrdinaryMessagesNeedNoReceipts(t *testing.T) {
	s, mock := unreadServer(t)
	unreadAccess(mock, 42)
	mock.ExpectBegin()
	unreadLock(mock, 1, 2).WillReturnRows(unreadRows().AddRow(1, 42, 0).AddRow(2, 42, 1))
	mock.ExpectCommit()
	unreadGet(mock, 42, 4)
	result, err := s.MarkTeamGroupMessagesRead(historyContext(t), unreadRequest(2, 1, 2))
	if err != nil || result.GetUnreadCount() != 4 || !reflect.DeepEqual(result.GetMessageIds(), []int64{1, 2}) {
		t.Fatalf("own message read=%v error=%v", result, err)
	}
}

func TestTeamGroupUnreadRejectsInvalidInputAndCredentialsBeforeSQL(t *testing.T) {
	s, _ := unreadServer(t)
	for _, req := range []*pb.GetTeamGroupUnreadRequest{nil, {TeamId: 0, GroupId: 300}, {TeamId: 200, GroupId: -1}} {
		result, err := s.GetTeamGroupUnread(historyContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid GET=%v error=%v", result, err)
		}
	}
	for _, req := range []*pb.MarkTeamGroupMessagesReadRequest{nil, {GroupId: 300, MessageIds: []int64{1}}, {TeamId: 200, MessageIds: []int64{1}}, unreadRequest(), unreadRequest(0), unreadRequest(1, -1), unreadRequest(make([]int64, 101)...)} {
		result, err := s.MarkTeamGroupMessagesRead(historyContext(t), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid Mark=%v error=%v", result, err)
		}
	}
	wrongToken, err := pkgjwt.GenerateToken(42, "wrong secret", 1)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := pkgjwt.GenerateToken(42, imTestSecret, -1)
	if err != nil {
		t.Fatal(err)
	}
	for _, headers := range [][]string{nil, {"Bearer invalid"}, {"Bearer " + wrongToken}, {"Bearer " + expired}, {"Bearer " + validIMToken(t), "Bearer " + validIMToken(t)}} {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"authorization": headers})
		get, getErr := s.GetTeamGroupUnread(ctx, &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300})
		mark, markErr := s.MarkTeamGroupMessagesRead(ctx, unreadRequest(1))
		if get != nil || mark != nil || status.Code(getErr) != codes.Unauthenticated || status.Code(markErr) != codes.Unauthenticated {
			t.Fatalf("invalid credential GET=%v/%v Mark=%v/%v", get, getErr, mark, markErr)
		}
	}
}

func TestTeamGroupUnreadDisabledAndWrongScopeFailClosed(t *testing.T) {
	for _, missing := range []string{"database", "secret", "team client"} {
		s, _ := unreadServer(t)
		switch missing {
		case "database":
			s.db = nil
		case "secret":
			s.jwtSecret = ""
		case "team client":
			s.teamClient = nil
		}
		get, getErr := s.GetTeamGroupUnread(historyContext(t), &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300})
		mark, markErr := s.MarkTeamGroupMessagesRead(historyContext(t), unreadRequest(1))
		if get != nil || mark != nil || status.Code(getErr) != codes.Unavailable || status.Code(markErr) != codes.Unavailable {
			t.Fatalf("disabled %s GET=%v/%v Mark=%v/%v", missing, get, getErr, mark, markErr)
		}
	}
	for _, method := range []string{"Get", "Mark"} {
		for _, scope := range []string{"left group", "left team", "legacy group", "other team"} {
			t.Run(method+"/"+scope, func(t *testing.T) {
				s, mock := unreadServer(t)
				rows := sqlmock.NewRows([]string{"team_id"})
				teamID := any(int64(200))
				if scope == "legacy group" {
					teamID = nil
				}
				if scope == "other team" {
					teamID = int64(201)
				}
				if scope != "left group" {
					rows.AddRow(teamID)
				}
				mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(rows)
				want := codes.NotFound
				if scope == "left group" || scope == "left team" {
					want = codes.PermissionDenied
				}
				if scope == "left team" {
					s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error {
						return status.Error(codes.PermissionDenied, "left team")
					})
				}
				if want == codes.NotFound {
					mock.ExpectQuery(regexp.QuoteMeta(historyGroupQuery)).WithArgs(int64(300), 1).WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(teamID))
				}
				var result any
				var err error
				if method == "Get" {
					r, e := s.GetTeamGroupUnread(historyContext(t), &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300})
					if r != nil {
						result = r
					}
					err = e
				} else {
					r, e := s.MarkTeamGroupMessagesRead(historyContext(t), unreadRequest(1))
					if r != nil {
						result = r
					}
					err = e
				}
				if result != nil || status.Code(err) != want {
					t.Fatalf("scope=%v error=%v", result, err)
				}
			})
		}
	}
}

func TestTeamGroupReadUnknownCrossGroupAndDirectMessagesRollbackWholeBatch(t *testing.T) {
	for _, excluded := range []string{"unknown message", "other group message", "direct message"} {
		t.Run(excluded, func(t *testing.T) {
			s, mock := unreadServer(t)
			unreadAccess(mock, 42)
			mock.ExpectBegin()
			// The scoped query returns only valid ID=1. ID=2 is absent for
			// each of these reasons, and must not confirm the valid member.
			unreadLock(mock, 1, 2).WillReturnRows(unreadRows().AddRow(1, 7, 1))
			mock.ExpectRollback()
			result, err := s.MarkTeamGroupMessagesRead(historyContext(t), unreadRequest(2, 1, 2))
			if result != nil || status.Code(err) != codes.NotFound {
				t.Fatalf("mixed batch=%v error=%v", result, err)
			}
		})
	}
}

func TestTeamGroupReadAccepts100MessagesAndDoesNotMutateRequest(t *testing.T) {
	s, mock := unreadServer(t)
	ids := make([]int64, 100)
	rows := unreadRows()
	for i := range ids {
		ids[i] = int64(i + 1)
		rows.AddRow(ids[i], 7, 1)
	}
	unreadAccess(mock, 42)
	mock.ExpectBegin()
	unreadLock(mock, ids...).WillReturnRows(rows)
	unreadInsert(mock, 42, ids...).WillReturnResult(sqlmock.NewResult(0, 100))
	mock.ExpectCommit()
	unreadGet(mock, 42, 0)
	reversed := make([]int64, len(ids))
	for i := range ids {
		reversed[i] = ids[len(ids)-1-i]
	}
	req := unreadRequest(reversed...)
	result, err := s.MarkTeamGroupMessagesRead(historyContext(t), req)
	if err != nil || !reflect.DeepEqual(result.GetMessageIds(), ids) || !reflect.DeepEqual(req.MessageIds, reversed) {
		t.Fatalf("100 messages=%v error=%v", result, err)
	}
}

func TestTeamGroupUnreadDatabaseFailureAndNegativeCountReturnNoResult(t *testing.T) {
	for _, negative := range []bool{false, true} {
		s, mock := unreadServer(t)
		unreadAccess(mock, 42)
		query := mock.ExpectQuery(regexp.QuoteMeta(unreadCountQuery)).WithArgs(int64(300), int64(42), int64(42), int64(300))
		if negative {
			query.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(-1))
		} else {
			query.WillReturnError(errors.New("private body token database-detail"))
		}
		result, err := s.GetTeamGroupUnread(historyContext(t), &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300})
		if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "database-detail") {
			t.Fatalf("failed count=%v error=%v", result, err)
		}
	}
}

func TestTeamGroupReadTransactionFailuresNeverClaimSuccess(t *testing.T) {
	for _, stage := range []string{"begin", "locked query", "receipt insert", "commit"} {
		t.Run(stage, func(t *testing.T) {
			s, mock := unreadServer(t)
			unreadAccess(mock, 42)
			privateErr := errors.New("private body token database-detail")
			if stage == "begin" {
				mock.ExpectBegin().WillReturnError(privateErr)
			} else {
				mock.ExpectBegin()
				if stage == "locked query" {
					unreadLock(mock, 1).WillReturnError(privateErr)
					mock.ExpectRollback()
				} else {
					unreadLock(mock, 1).WillReturnRows(unreadRows().AddRow(1, 7, 1))
					if stage == "receipt insert" {
						unreadInsert(mock, 42, 1).WillReturnError(privateErr)
						mock.ExpectRollback()
					} else {
						unreadInsert(mock, 42, 1).WillReturnResult(sqlmock.NewResult(0, 1))
						mock.ExpectCommit().WillReturnError(privateErr)
					}
				}
			}
			result, err := s.MarkTeamGroupMessagesRead(historyContext(t), unreadRequest(1))
			if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "database-detail") {
				t.Fatalf("transaction failure=%v error=%v", result, err)
			}
		})
	}
}

func TestTeamGroupUnreadRechecksQualificationAfterCountAndCommit(t *testing.T) {
	for _, stage := range []string{"after GET count", "after Mark commit", "after Mark count"} {
		for _, denied := range []string{"group", "team"} {
			t.Run(stage+"/"+denied, func(t *testing.T) {
				s, mock := unreadServer(t)
				checks := 0
				failureCheck := 2
				if stage == "after Mark count" {
					failureCheck = 3
				}
				s.teamClient = teamCheckFunc(func(context.Context, *userpb.CheckTeamMemberRequest) error {
					checks++
					if denied == "team" && checks == failureCheck {
						return status.Error(codes.PermissionDenied, "left team")
					}
					return nil
				})
				unreadAccess(mock, 42)
				if stage != "after GET count" {
					mock.ExpectBegin()
					unreadLock(mock, 1).WillReturnRows(unreadRows().AddRow(1, 7, 1))
					unreadInsert(mock, 42, 1).WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectCommit()
				}
				if stage == "after GET count" || stage == "after Mark count" {
					if stage == "after Mark count" {
						unreadAccess(mock, 42)
					}
					unreadCount(mock, 42, 5)
				}
				rows := sqlmock.NewRows([]string{"team_id"})
				if denied == "team" {
					rows.AddRow(200)
				}
				mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).WillReturnRows(rows)
				var result any
				var err error
				if stage == "after GET count" {
					r, e := s.GetTeamGroupUnread(historyContext(t), &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300})
					if r != nil {
						result = r
					}
					err = e
				} else {
					r, e := s.MarkTeamGroupMessagesRead(historyContext(t), unreadRequest(1))
					if r != nil {
						result = r
					}
					err = e
				}
				if result != nil || status.Code(err) != codes.PermissionDenied {
					t.Fatalf("revoked result=%v error=%v", result, err)
				}
			})
		}
	}
}

func TestTeamGroupReadCountFailureAfterCommitDoesNotClaimNoWrite(t *testing.T) {
	s, mock := unreadServer(t)
	unreadAccess(mock, 42)
	mock.ExpectBegin()
	unreadLock(mock, 1).WillReturnRows(unreadRows().AddRow(1, 7, 1))
	unreadInsert(mock, 42, 1).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	unreadAccess(mock, 42)
	mock.ExpectQuery(regexp.QuoteMeta(unreadCountQuery)).WithArgs(int64(300), int64(42), int64(42), int64(300)).WillReturnError(errors.New("private database-detail"))
	result, err := s.MarkTeamGroupMessagesRead(historyContext(t), unreadRequest(1))
	if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "database-detail") {
		t.Fatalf("postcommit count=%v error=%v", result, err)
	}
}

func TestTeamGroupReadCancelledOrExpiredSQLRollsBackWithoutReceipt(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		s, mock := unreadServer(t)
		unreadAccess(mock, 42)
		mock.ExpectBegin()
		unreadLock(mock, 1).WillDelayFor(time.Second).WillReturnRows(unreadRows().AddRow(1, 7, 1))
		mock.ExpectRollback()
		ctx, cancel := context.WithCancel(historyContext(t))
		want := codes.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(historyContext(t), 100*time.Millisecond)
			want = codes.DeadlineExceeded
		} else {
			timer := time.AfterFunc(100*time.Millisecond, cancel)
			defer timer.Stop()
		}
		result, err := s.MarkTeamGroupMessagesRead(ctx, unreadRequest(1))
		cancel()
		if result != nil || status.Code(err) != want {
			t.Fatalf("cancelled/expired SQL=%v error=%v", result, err)
		}
		// database/sql may roll back in its cancellation goroutine. Wait for
		// the observed rollback before the shared helper checks expectations.
		until := time.Now().Add(2 * time.Second)
		for {
			if err := mock.ExpectationsWereMet(); err == nil {
				break
			} else if time.Now().After(until) {
				t.Fatal(err)
			}
			time.Sleep(time.Millisecond)
		}
	}
}
