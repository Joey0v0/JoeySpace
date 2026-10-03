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

const listTasksQuery = "SELECT id, title, description, creator_id, assignee_id, status, source_group_id, source_message_id, due_at_unix_ms FROM `tasks` WHERE team_id = ? AND id > ? ORDER BY id ASC LIMIT ?"

func taskListContext() context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer sample"))
}

func TestListTeamTasksOverRPCWithCursor(t *testing.T) {
	s, mock := testTaskServer(t)
	s.teamClient = teamCheckerFake{
		checkMember: func(ctx context.Context, teamID int64) (*userpb.CheckTeamMemberResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if teamID != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer sample" {
				t.Errorf("wrong membership request: team=%d, metadata=%v", teamID, md)
			}
			return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
		},
	}
	mock.ExpectQuery(regexp.QuoteMeta(listTasksQuery)).WithArgs(int64(200), int64(10), 3).
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "description", "creator_id", "assignee_id", "status", "source_group_id", "source_message_id", "due_at_unix_ms"}).
			AddRow(int64(11), "Planning", "First", int64(42), int64(77), int8(0), int64(300), int64(400), int64(1790874000000)).
			AddRow(int64(12), "Review", "Second", int64(43), nil, int8(1), nil, nil, nil).
			AddRow(int64(13), "Later", "Third", int64(43), nil, int8(2), nil, nil, nil))
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
	result, err := pb.NewTaskClient(conn).ListTeamTasks(ctx, &pb.ListTeamTasksRequest{TeamId: 200, AfterTaskId: 10, Limit: 2})
	if err != nil || len(result.GetTasks()) != 2 || result.GetTasks()[0].GetTaskId() != 11 || result.GetTasks()[0].GetAssigneeId() != 77 || result.GetTasks()[0].GetSourceGroupId() != 300 || result.GetTasks()[0].GetSourceMessageId() != 400 || result.GetTasks()[0].GetDueAtUnixMs() != 1790874000000 || result.GetTasks()[1].GetAssigneeId() != 0 || result.GetTasks()[1].GetSourceGroupId() != 0 || result.GetTasks()[1].GetDueAtUnixMs() != 0 || result.GetTasks()[1].GetStatus() != 1 || result.GetNextAfterTaskId() != 12 {
		t.Fatalf("task page: %v, %v", result, err)
	}
}

func TestListTeamTasksRejectsInvalidAndNonMember(t *testing.T) {
	s, _ := testTaskServer(t)
	for _, req := range []*pb.ListTeamTasksRequest{nil, {TeamId: 0}, {TeamId: 200, AfterTaskId: -1}, {TeamId: 200, Limit: -1}, {TeamId: 200, Limit: 101}} {
		result, err := s.ListTeamTasks(taskListContext(), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid list: %v, %v", result, err)
		}
	}
	result, err := s.ListTeamTasks(context.Background(), &pb.ListTeamTasksRequest{TeamId: 200})
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing login: %v, %v", result, err)
	}
	s.teamClient = teamCheckerFake{
		checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "not in team")
		},
	}
	result, err = s.ListTeamTasks(taskListContext(), &pb.ListTeamTasksRequest{TeamId: 200})
	if result != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-member: %v, %v", result, err)
	}
}

func TestListTeamTasksEmptyAndDatabaseFailure(t *testing.T) {
	s, mock := testTaskServer(t)
	mock.ExpectQuery(regexp.QuoteMeta(listTasksQuery)).WithArgs(int64(200), int64(0), 21).
		WillReturnRows(sqlmock.NewRows([]string{"id", "title", "description", "creator_id", "assignee_id", "status", "source_group_id", "source_message_id", "due_at_unix_ms"}))
	result, err := s.ListTeamTasks(taskListContext(), &pb.ListTeamTasksRequest{TeamId: 200})
	if err != nil || result == nil || len(result.GetTasks()) != 0 || result.GetNextAfterTaskId() != 0 {
		t.Fatalf("empty team task list: %v, %v", result, err)
	}
	mock.ExpectQuery(regexp.QuoteMeta(listTasksQuery)).WithArgs(int64(200), int64(0), 21).
		WillReturnError(errors.New("private DB detail"))
	result, err = s.ListTeamTasks(taskListContext(), &pb.ListTeamTasksRequest{TeamId: 200})
	if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() == "private DB detail" {
		t.Fatalf("database failure: %v, %v", result, err)
	}
}
