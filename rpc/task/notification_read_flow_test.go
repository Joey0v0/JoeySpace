package main

import (
	"context"
	"errors"
	"math"
	"net"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestNotificationReadFlowOverRPCLostResponseAndMembershipRecovery(t *testing.T) {
	s, mock := testTaskServer(t)
	const notificationID int64 = math.MaxInt64
	const taskID int64 = math.MaxInt64 - 10
	var revoked atomic.Bool
	var memberChecks atomic.Int64
	s.teamClient = teamCheckerFake{checkMember: func(ctx context.Context, teamID int64) (*userpb.CheckTeamMemberResponse, error) {
		memberChecks.Add(1)
		md, _ := metadata.FromOutgoingContext(ctx)
		if teamID != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer sample" || len(md.Get("recipient-id")) != 0 {
			return nil, status.Error(codes.Internal, "unexpected membership identity scope")
		}
		if _, ok := ctx.Deadline(); !ok {
			return nil, status.Error(codes.Internal, "membership call has no deadline")
		}
		if revoked.Load() {
			return nil, status.Error(codes.PermissionDenied, "team membership required")
		}
		return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
	}}

	// SQL and User remain substitutes. Only the first successful reply is lost;
	// the interceptor runs after the production handler has committed and rechecked membership.
	var responseLost atomic.Bool
	committedResponse := make(chan *pb.MarkTaskNotificationReadResponse, 1)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		result, err := handler(ctx, req)
		if info.FullMethod != pb.Task_MarkTaskNotificationRead_FullMethodName || err != nil || responseLost.Load() {
			return result, err
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			return nil, status.Error(codes.Internal, "read transaction did not finish before reply")
		}
		saved, ok := result.(*pb.MarkTaskNotificationReadResponse)
		if !ok || saved.NotificationId != notificationID || saved.ReadAtUnixMs != noticeRead {
			return nil, status.Error(codes.Internal, "unexpected committed read result")
		}
		if responseLost.CompareAndSwap(false, true) {
			committedResponse <- saved
			return nil, status.Error(codes.Unavailable, "simulated read acknowledgement lost")
		}
		return result, nil
	}))
	pb.RegisterTaskServer(server, s)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		select {
		case err := <-serveDone:
			if err != nil && !errors.Is(err, grpc.ErrServerStopped) && !errors.Is(err, net.ErrClosed) {
				t.Errorf("Task RPC Serve: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Task RPC Serve did not stop")
		}
	})
	go func() { serveDone <- server.Serve(listener) }()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close Task RPC connection: %v", err)
		}
	})
	client := pb.NewTaskClient(conn)
	requestContext := func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		// A caller-supplied recipient cannot override the user resolved from this Token.
		return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer sample", "recipient-id", "77")), cancel
	}
	list := func() (*pb.ListTaskNotificationsResponse, error) {
		ctx, cancel := requestContext()
		defer cancel()
		return client.ListTaskNotifications(ctx, &pb.ListTaskNotificationsRequest{TeamId: 200, Limit: 1})
	}
	mark := func() (*pb.MarkTaskNotificationReadResponse, error) {
		ctx, cancel := requestContext()
		defer cancel()
		return client.MarkTaskNotificationRead(ctx, &pb.MarkTaskNotificationReadRequest{TeamId: 200, NotificationId: notificationID})
	}
	expectList := func(readAt int64) {
		mock.ExpectQuery(regexp.QuoteMeta(notificationListQuery)).WithArgs(int64(200), int64(42), 2).
			WillReturnRows(notificationRows().AddRow(notificationID, taskID, int64(77), 0, 2, noticeCreated, readAt))
	}
	checkList := func(readAt int64) {
		t.Helper()
		result, err := list()
		if err != nil || result == nil || len(result.Notifications) != 1 || result.NextBeforeNotificationId != 0 {
			t.Fatalf("notification page=%v error=%v", result, err)
		}
		item := result.Notifications[0]
		if item.NotificationId != notificationID || item.TaskId != taskID || item.ActorId != 77 || item.FromStatus != 0 || item.ToStatus != 2 || item.CreatedAtUnixMs != noticeCreated || item.ReadAtUnixMs != readAt {
			t.Fatalf("notification row changed during recovery: %v; want read_at=%d", item, readAt)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}

	expectList(0)
	checkList(0)
	expectUnreadNotification(mock, notificationID)
	expectNotificationReadWrite(mock, notificationID, nil)
	result, err := mark()
	if result != nil || status.Code(err) != codes.Unavailable || !responseLost.Load() {
		t.Fatalf("lost committed acknowledgement=%v error=%v lost=%v", result, err, responseLost.Load())
	}
	select {
	case saved := <-committedResponse:
		if saved.NotificationId != notificationID || saved.ReadAtUnixMs != noticeRead {
			t.Fatalf("intercepted acknowledgement=%v", saved)
		}
	default:
		t.Fatal("successful committed response was not intercepted")
	}

	// The query is the authority after an uncertain reply. Retry reads the same
	// locked row and commits without UPDATE or a new timestamp.
	expectList(noticeRead)
	checkList(noticeRead)
	expectAlreadyReadNotification(mock, notificationID)
	result, err = mark()
	if err != nil || result == nil || result.NotificationId != notificationID || result.ReadAtUnixMs != noticeRead {
		t.Fatalf("idempotent read retry=%v error=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	// No SQL expectations are admitted while revoked; both RPCs must stop at
	// their initial User authorization check instead of attempting database work.
	revoked.Store(true)
	deniedPage, err := list()
	if deniedPage != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("revoked list=%v error=%v", deniedPage, err)
	}
	result, err = mark()
	if result != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("revoked mark=%v error=%v", result, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	revoked.Store(false)
	expectList(noticeRead)
	checkList(noticeRead)
	if checks := memberChecks.Load(); checks != 12 {
		t.Fatalf("membership checks=%d; want two per successful handler and one per revoked handler", checks)
	}
}
