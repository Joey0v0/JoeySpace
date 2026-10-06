package main

import (
	"context"
	"math"
	"net"
	"reflect"
	"regexp"
	"sync/atomic"
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

const unreadRecoveryBotID int64 = 9007199254740993

func unreadRecoveryCommit(mock sqlmock.Sqlmock, affected int64) {
	unreadAccess(mock, 42)
	mock.ExpectBegin()
	unreadLock(mock, 100, 101, unreadRecoveryBotID, math.MaxInt64).WillReturnRows(unreadRows().
		AddRow(100, 42, 0).AddRow(101, 42, 1).
		AddRow(unreadRecoveryBotID, 42, 2).AddRow(math.MaxInt64, 7, 1))
	// Anchors reject any extra read_at update or application-provided timestamp.
	// The bot's numeric sender ID equals the reader, but it still needs a receipt.
	query := "INSERT INTO `im_group_message_reads` (`user_id`,`group_id`,`message_id`) VALUES (?,?,?),(?,?,?) ON DUPLICATE KEY UPDATE `user_id`=`user_id`"
	mock.ExpectExec("^"+regexp.QuoteMeta(query)+"$").
		WithArgs(int64(42), int64(300), unreadRecoveryBotID, int64(42), int64(300), int64(math.MaxInt64)).
		WillReturnResult(sqlmock.NewResult(0, affected))
	mock.ExpectCommit()
}

func unreadRecoveryGet(mock sqlmock.Sqlmock, count int64) {
	unreadAccess(mock, 42)
	// Exact SQL excludes only specific receipts, never an ID/time watermark.
	mock.ExpectQuery("^"+regexp.QuoteMeta(unreadCountQuery)+"$").
		WithArgs(int64(300), int64(42), int64(42), int64(300)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
	unreadAccess(mock, 42)
}

func unreadRecoveryClient(t *testing.T, impl *imServer, authorization string, interceptor grpc.UnaryServerInterceptor) pb.IMClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if values := md.Get("authorization"); len(values) != 1 || values[0] != authorization || !reflect.DeepEqual(md.Get("user_id"), []string{"999"}) {
			t.Error("recovery RPC did not receive the original Bearer and spoofed reader test header")
		}
		if interceptor != nil {
			return interceptor(ctx, req, info, next)
		}
		return next(ctx, req)
	}))
	pb.RegisterIMServer(server, impl)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("unread recovery gRPC server did not stop")
		}
	})
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewIMClient(conn)
}

func unreadRecoveryContext(t *testing.T, authorization string) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", authorization, "user_id", "999"))
}

func unreadRecoveryTeam(t *testing.T, authorization string, check func() error) teamCheckFunc {
	t.Helper()
	return teamCheckFunc(func(ctx context.Context, req *userpb.CheckTeamMemberRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || !reflect.DeepEqual(md, metadata.Pairs("authorization", authorization)) {
			t.Error("production IM did not forward only the original Bearer to the current team check")
			return status.Error(codes.Unauthenticated, "invalid test forwarding")
		}
		return check()
	})
}

func unreadRecoveryRequest() *pb.MarkTeamGroupMessagesReadRequest {
	return unreadRequest(math.MaxInt64, unreadRecoveryBotID, 101, 100, unreadRecoveryBotID)
}

func assertUnreadRecoveryMark(t *testing.T, result *pb.MarkTeamGroupMessagesReadResponse, err error) {
	t.Helper()
	if err != nil || result.GetTeamId() != 200 || result.GetGroupId() != 300 || result.GetUnreadCount() != 1 ||
		!reflect.DeepEqual(result.GetMessageIds(), []int64{100, 101, unreadRecoveryBotID, math.MaxInt64}) {
		t.Fatalf("explicit recovery mark = %v, error = %v", result, err)
	}
}

func assertUnreadRecoveryGet(t *testing.T, result *pb.GetTeamGroupUnreadResponse, err error, count int64) {
	t.Helper()
	if err != nil || result.GetTeamId() != 200 || result.GetGroupId() != 300 || result.GetUnreadCount() != count {
		t.Fatalf("recovery count = %v, error = %v", result, err)
	}
}

func assertUnreadRecoverySQL(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTeamGroupUnreadTCPRecoveryAfterCommittedMarkResponseLoss(t *testing.T) {
	impl, mock := unreadServer(t)
	authorization := "Bearer " + validIMToken(t)
	impl.teamClient = unreadRecoveryTeam(t, authorization, func() error { return nil })
	var lost atomic.Bool
	client := unreadRecoveryClient(t, impl, authorization, func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		result, err := next(ctx, req)
		if info.FullMethod == pb.IM_MarkTeamGroupMessagesRead_FullMethodName && err == nil && lost.CompareAndSwap(false, true) {
			marked, ok := result.(*pb.MarkTeamGroupMessagesReadResponse)
			if !ok || marked.GetTeamId() != 200 || marked.GetGroupId() != 300 || marked.GetUnreadCount() != 1 ||
				!reflect.DeepEqual(marked.GetMessageIds(), []int64{100, 101, unreadRecoveryBotID, math.MaxInt64}) {
				t.Error("response-loss injector did not observe the expected completed production Mark")
			}
			// The SQL commit and all post-commit checks completed before this
			// test-only transport failure replaces the successful response.
			return nil, status.Error(codes.Unavailable, "simulated committed response loss")
		}
		return result, err
	})
	ctx := unreadRecoveryContext(t, authorization)
	get := &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300}
	unreadRecoveryGet(mock, 3)
	before, err := client.GetTeamGroupUnread(ctx, get)
	assertUnreadRecoveryGet(t, before, err, 3)
	assertUnreadRecoverySQL(t, mock)
	unreadRecoveryCommit(mock, 2)
	unreadRecoveryGet(mock, 1)
	marked, err := client.MarkTeamGroupMessagesRead(ctx, unreadRecoveryRequest())
	if marked != nil || status.Code(err) != codes.Unavailable || !lost.Load() {
		t.Fatalf("committed response-loss window = %v, error = %v", marked, err)
	}
	assertUnreadRecoverySQL(t, mock)
	// The caller first reads its state; GET has only read expectations.
	// These returned counts model retained SQL state, not a real MySQL restart.
	unreadRecoveryGet(mock, 1)
	recovered, err := client.GetTeamGroupUnread(ctx, get)
	assertUnreadRecoveryGet(t, recovered, err, 1)
	assertUnreadRecoverySQL(t, mock)
	unreadRecoveryCommit(mock, 0)
	unreadRecoveryGet(mock, 1)
	marked, err = client.MarkTeamGroupMessagesRead(ctx, unreadRecoveryRequest())
	assertUnreadRecoveryMark(t, marked, err)
	assertUnreadRecoverySQL(t, mock)
}

func TestTeamGroupUnreadTCPRecoveryAfterPostCommitRevocationAndRejoin(t *testing.T) {
	impl, mock := unreadServer(t)
	authorization := "Bearer " + validIMToken(t)
	var checks atomic.Int64
	var revoked atomic.Bool
	impl.teamClient = unreadRecoveryTeam(t, authorization, func() error {
		if checks.Add(1) == 2 {
			revoked.Store(true)
		}
		if revoked.Load() {
			return status.Error(codes.PermissionDenied, "current team membership revoked")
		}
		return nil
	})
	client := unreadRecoveryClient(t, impl, authorization, nil)
	ctx := unreadRecoveryContext(t, authorization)
	get := &pb.GetTeamGroupUnreadRequest{TeamId: 200, GroupId: 300}
	unreadRecoveryCommit(mock, 2)
	// The group-members row remains. User denies the second qualification
	// check, which SQL order proves happens after the receipt commit.
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
	marked, err := client.MarkTeamGroupMessagesRead(ctx, unreadRecoveryRequest())
	if marked != nil || status.Code(err) != codes.PermissionDenied || checks.Load() != 2 {
		t.Fatalf("post-commit denial = %v, error = %v", marked, err)
	}
	assertUnreadRecoverySQL(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta(memberQuery)).WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"team_id"}).AddRow(200))
	denied, err := client.GetTeamGroupUnread(ctx, get)
	if denied != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("revoked reader count = %v, error = %v", denied, err)
	}
	assertUnreadRecoverySQL(t, mock)
	// Restoring the User substitute models current qualification only. It
	// makes no claim about real leave/rejoin cleanup or atomic cross-service ACLs.
	revoked.Store(false)
	unreadRecoveryGet(mock, 1)
	recovered, err := client.GetTeamGroupUnread(ctx, get)
	assertUnreadRecoveryGet(t, recovered, err, 1)
	assertUnreadRecoverySQL(t, mock)
	unreadRecoveryCommit(mock, 0)
	unreadRecoveryGet(mock, 1)
	marked, err = client.MarkTeamGroupMessagesRead(ctx, unreadRecoveryRequest())
	assertUnreadRecoveryMark(t, marked, err)
	assertUnreadRecoverySQL(t, mock)
	if checks.Load() != 8 {
		t.Fatalf("current team checks = %d, want 8", checks.Load())
	}
}
