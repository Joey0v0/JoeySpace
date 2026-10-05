package main

import (
	"context"
	"errors"
	"net"
	"regexp"
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

const taskStatusQuery = "SELECT id, creator_id, assignee_id, status FROM `tasks` WHERE id = ? AND team_id = ? LIMIT ? FOR UPDATE"

func statusTaskServer(t *testing.T, actorID int64, role int32) (*taskServer, sqlmock.Sqlmock) {
	t.Helper()
	s, mock := testTaskServer(t)
	s.teamClient = teamCheckerFake{
		checkMember: func(ctx context.Context, teamID int64) (*userpb.CheckTeamMemberResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if teamID != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer sample" {
				t.Errorf("wrong team check: team=%d, auth=%v", teamID, md.Get("authorization"))
			}
			return &userpb.CheckTeamMemberResponse{UserId: actorID, Role: role}, nil
		},
	}
	return s, mock
}

func expectTaskStatusRow(mock sqlmock.Sqlmock, creatorID int64, assigneeID any, oldStatus int8) {
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(taskStatusQuery)).WithArgs(int64(500), int64(200), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "creator_id", "assignee_id", "status"}).AddRow(int64(500), creatorID, assigneeID, oldStatus))
}

func expectTaskStatusChange(mock sqlmock.Sqlmock, actorID int64, oldStatus, newStatus int8, operationErr, notificationErr error, recipientIDs ...int64) {
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `tasks` SET `status`=? WHERE id = ? AND team_id = ?")).
		WithArgs(newStatus, int64(500), int64(200)).WillReturnResult(sqlmock.NewResult(0, 1))
	insert := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `task_operations`")).
		WithArgs(int64(500), actorID, oldStatus, newStatus)
	if operationErr != nil {
		insert.WillReturnError(operationErr)
		mock.ExpectRollback()
		return
	}
	insert.WillReturnResult(sqlmock.NewResult(123, 1))
	for i, recipientID := range recipientIDs {
		insert := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `task_status_notifications`")).
			WithArgs(int64(123), int64(200), int64(500), recipientID)
		if notificationErr != nil && i == len(recipientIDs)-1 {
			insert.WillReturnError(notificationErr)
			mock.ExpectRollback()
			return
		}
		insert.WillReturnResult(sqlmock.NewResult(int64(i+1), 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `task_notification_outbox`")).
			WithArgs(int64(i+1), int64(200), recipientID, 1, false).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()
}

func statusRequest(newStatus int32) *pb.SetTaskStatusRequest {
	return &pb.SetTaskStatusRequest{TeamId: 200, TaskId: 500, Status: newStatus}
}

func TestSetTaskStatusOverRPCWritesOperation(t *testing.T) {
	s, mock := statusTaskServer(t, 42, 0)
	expectTaskStatusRow(mock, 42, int64(77), 0)
	expectTaskStatusChange(mock, 42, 0, 1, nil, nil, 42, 77)
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
	result, err := pb.NewTaskClient(conn).SetTaskStatus(ctx, statusRequest(1))
	if err != nil || result == nil {
		t.Fatalf("status RPC: %v, %v", result, err)
	}
}

func TestSetTaskStatusAllowsAssigneeAndOwnerButNotOtherMember(t *testing.T) {
	for _, tc := range []struct {
		name    string
		actorID int64
		role    int32
		allowed bool
	}{
		{"assignee", 77, 0, true},
		{"owner", 88, 2, true},
		{"other member", 88, 0, false},
		{"administrator", 88, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := statusTaskServer(t, tc.actorID, tc.role)
			expectTaskStatusRow(mock, 42, int64(77), 0)
			if tc.allowed {
				expectTaskStatusChange(mock, tc.actorID, 0, 2, nil, nil, 42, 77)
			} else {
				mock.ExpectRollback()
			}
			result, err := s.SetTaskStatus(taskListContext(), statusRequest(2))
			if tc.allowed && (err != nil || result == nil) || !tc.allowed && (result != nil || status.Code(err) != codes.PermissionDenied) {
				t.Fatalf("actor %s: %v, %v", tc.name, result, err)
			}
		})
	}
}

func TestSetTaskStatusRejectsInvalidScopeAndMissingLogin(t *testing.T) {
	s, mock := statusTaskServer(t, 42, 0)
	for _, req := range []*pb.SetTaskStatusRequest{nil, {TeamId: 0, TaskId: 500}, {TeamId: 200, TaskId: 0}, {TeamId: 200, TaskId: 500, Status: -1}, {TeamId: 200, TaskId: 500, Status: 3}} {
		result, err := s.SetTaskStatus(taskListContext(), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %v, %v", result, err)
		}
	}
	result, err := s.SetTaskStatus(context.Background(), statusRequest(1))
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(taskStatusQuery)).WithArgs(int64(500), int64(200), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "creator_id", "assignee_id", "status"}))
	mock.ExpectRollback()
	result, err = s.SetTaskStatus(taskListContext(), statusRequest(1))
	if result != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("task outside team: %v, %v", result, err)
	}
}

func TestSetTaskStatusNoopAndOperationFailure(t *testing.T) {
	s, mock := statusTaskServer(t, 42, 0)
	expectTaskStatusRow(mock, 42, nil, 1)
	mock.ExpectCommit()
	result, err := s.SetTaskStatus(taskListContext(), statusRequest(1))
	if err != nil || result == nil {
		t.Fatalf("repeated status: %v, %v", result, err)
	}
	expectTaskStatusRow(mock, 42, nil, 1)
	expectTaskStatusChange(mock, 42, 1, 2, errors.New("operation insert failed"), nil)
	result, err = s.SetTaskStatus(taskListContext(), statusRequest(2))
	if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "operation insert failed" {
		t.Fatalf("operation failure: %v, %v", result, err)
	}
}

func TestSetTaskStatusNotificationRecipients(t *testing.T) {
	for _, tc := range []struct {
		name         string
		assigneeID   any
		recipientIDs []int64
	}{
		{"creator and assignee", int64(77), []int64{42, 77}},
		{"creator also assignee", int64(42), []int64{42}},
		{"unassigned", nil, []int64{42}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := statusTaskServer(t, 42, 0)
			expectTaskStatusRow(mock, 42, tc.assigneeID, 0)
			expectTaskStatusChange(mock, 42, 0, 1, nil, nil, tc.recipientIDs...)
			result, err := s.SetTaskStatus(taskListContext(), statusRequest(1))
			if err != nil || result == nil {
				t.Fatalf("status change: %v, %v", result, err)
			}
		})
	}
}

func TestSetTaskStatusNotificationFailureRollsBack(t *testing.T) {
	s, mock := statusTaskServer(t, 42, 0)
	expectTaskStatusRow(mock, 42, int64(77), 0)
	expectTaskStatusChange(mock, 42, 0, 1, nil, errors.New("notification insert failed"), 42, 77)
	result, err := s.SetTaskStatus(taskListContext(), statusRequest(1))
	if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "notification insert failed" {
		t.Fatalf("notification failure: %v, %v", result, err)
	}
}

func TestSetTaskStatusOutboxFailureRollsBackAllRecipients(t *testing.T) {
	for _, failRecipient := range []int64{42, 77} {
		t.Run(map[int64]string{42: "first recipient", 77: "second recipient"}[failRecipient], func(t *testing.T) {
			s, mock := statusTaskServer(t, 42, 0)
			expectTaskStatusRow(mock, 42, int64(77), 0)
			mock.ExpectExec(regexp.QuoteMeta("UPDATE `tasks` SET `status`=? WHERE id = ? AND team_id = ?")).
				WithArgs(int8(1), int64(500), int64(200)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `task_operations`")).
				WithArgs(int64(500), int64(42), int8(0), int8(1)).WillReturnResult(sqlmock.NewResult(123, 1))
			for i, recipientID := range []int64{42, 77} {
				id := int64(i + 1)
				mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `task_status_notifications`")).
					WithArgs(int64(123), int64(200), int64(500), recipientID).WillReturnResult(sqlmock.NewResult(id, 1))
				write := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `task_notification_outbox`")).WithArgs(id, int64(200), recipientID, 1, false)
				if recipientID == failRecipient {
					write.WillReturnError(errors.New("outbox table missing private detail"))
					break
				}
				write.WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectRollback()
			result, err := s.SetTaskStatus(taskListContext(), statusRequest(1))
			if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() != "task database unavailable" {
				t.Fatalf("event failure: result=%v error=%v", result, err)
			}
		})
	}
}

func TestSetTaskStatusInvalidNotificationIDPreventsOutboxAndRollsBack(t *testing.T) {
	for _, id := range []int64{0, -1} {
		s, mock := statusTaskServer(t, 42, 0)
		expectTaskStatusRow(mock, 42, nil, 0)
		mock.ExpectExec(regexp.QuoteMeta("UPDATE `tasks` SET `status`=? WHERE id = ? AND team_id = ?")).
			WithArgs(int8(1), int64(500), int64(200)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `task_operations`")).
			WithArgs(int64(500), int64(42), int8(0), int8(1)).WillReturnResult(sqlmock.NewResult(123, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `task_status_notifications`")).
			WithArgs(int64(123), int64(200), int64(500), int64(42)).WillReturnResult(sqlmock.NewResult(id, 1))
		mock.ExpectRollback()
		result, err := s.SetTaskStatus(taskListContext(), statusRequest(1))
		if result != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("invalid notice ID %d: result=%v error=%v", id, result, err)
		}
	}
}
