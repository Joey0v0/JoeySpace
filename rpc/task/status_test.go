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

func expectTaskStatusChange(mock sqlmock.Sqlmock, actorID int64, oldStatus, newStatus int8, operationErr error) {
	mock.ExpectExec(regexp.QuoteMeta("UPDATE `tasks` SET `status`=? WHERE id = ? AND team_id = ?")).
		WithArgs(newStatus, int64(500), int64(200)).WillReturnResult(sqlmock.NewResult(0, 1))
	insert := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `task_operations`")).
		WithArgs(int64(500), actorID, oldStatus, newStatus)
	if operationErr != nil {
		insert.WillReturnError(operationErr)
		mock.ExpectRollback()
	} else {
		insert.WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
	}
}

func statusRequest(newStatus int32) *pb.SetTaskStatusRequest {
	return &pb.SetTaskStatusRequest{TeamId: 200, TaskId: 500, Status: newStatus}
}

func TestSetTaskStatusOverRPCWritesOperation(t *testing.T) {
	s, mock := statusTaskServer(t, 42, 0)
	expectTaskStatusRow(mock, 42, int64(77), 0)
	expectTaskStatusChange(mock, 42, 0, 1, nil)
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
				expectTaskStatusChange(mock, tc.actorID, 0, 2, nil)
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
	expectTaskStatusChange(mock, 42, 1, 2, errors.New("operation insert failed"))
	result, err = s.SetTaskStatus(taskListContext(), statusRequest(2))
	if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "operation insert failed" {
		t.Fatalf("operation failure: %v, %v", result, err)
	}
}
