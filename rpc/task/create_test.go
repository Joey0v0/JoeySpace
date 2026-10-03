package main

import (
	"context"
	"database/sql/driver"
	"errors"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/bwmarrin/snowflake"
	driverMysql "github.com/go-sql-driver/mysql"
	impb "github.com/yjydist/go-im/rpc/im/pb"
	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type positiveTaskID struct{}

func (positiveTaskID) Match(value driver.Value) bool {
	id, ok := value.(int64)
	return ok && id > 0
}

type teamCheckerFake struct {
	checkMember func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error)
	checkTarget func(context.Context, int64, int64) error
}

type messageCheckerFake func(context.Context, *impb.CheckTeamGroupMessageRequest) error

func (f messageCheckerFake) CheckTeamGroupMessage(ctx context.Context, req *impb.CheckTeamGroupMessageRequest, _ ...grpc.CallOption) (*impb.CheckTeamGroupMessageResponse, error) {
	if err := f(ctx, req); err != nil {
		return nil, err
	}
	return &impb.CheckTeamGroupMessageResponse{}, nil
}

func (f teamCheckerFake) CheckTeamMember(ctx context.Context, req *userpb.CheckTeamMemberRequest, _ ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	return f.checkMember(ctx, req.GetTeamId())
}

func (f teamCheckerFake) CheckTeamMemberByID(ctx context.Context, req *userpb.CheckTeamMemberByIDRequest, _ ...grpc.CallOption) (*userpb.CheckTeamMemberByIDResponse, error) {
	if err := f.checkTarget(ctx, req.GetTeamId(), req.GetUserId()); err != nil {
		return nil, err
	}
	return &userpb.CheckTeamMemberByIDResponse{}, nil
}

func testTaskServer(t *testing.T) (*taskServer, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
	node, err := snowflake.NewNode(4)
	if err != nil {
		t.Fatal(err)
	}
	s := &taskServer{db: db, idNode: node}
	s.teamClient = teamCheckerFake{
		checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
			return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
		},
		checkTarget: func(context.Context, int64, int64) error { return nil },
	}
	return s, mock
}

func taskContext() context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer sample", "idempotency-key", "request-123"))
}

func taskRequest() *pb.CreateTaskRequest {
	return &pb.CreateTaskRequest{TeamId: 200, Title: " Planning ", Description: " Check tasks ", AssigneeId: 77}
}

func expectTaskInsert(mock sqlmock.Sqlmock, insertErr error) {
	fingerprint, _ := taskRequestFingerprint(200, "Planning", "Check tasks", 77, 0, 0, 0)
	mock.ExpectBegin()
	insert := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `tasks`"))
	insert.WithArgs(int64(200), "Planning", "Check tasks", int64(42), int64(77), nil, nil, nil, int8(0), "request-123", fingerprint, positiveTaskID{})
	if insertErr != nil {
		insert.WillReturnError(insertErr)
		mock.ExpectRollback()
	} else {
		insert.WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
	}
}

func expectTaskNotFound(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, request_hash FROM `tasks` WHERE creator_id = ? AND request_key = ?")).
		WithArgs(int64(42), "request-123", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "request_hash"}))
}

func TestCreateTaskOverRPC(t *testing.T) {
	s, mock := testTaskServer(t)
	s.teamClient = teamCheckerFake{
		checkMember: func(ctx context.Context, teamID int64) (*userpb.CheckTeamMemberResponse, error) {
			md, _ := metadata.FromOutgoingContext(ctx)
			if teamID != 200 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer sample" {
				t.Errorf("wrong caller check: team=%d auth=%v", teamID, md.Get("authorization"))
			}
			return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
		},
		checkTarget: func(ctx context.Context, teamID, userID int64) error {
			md, _ := metadata.FromOutgoingContext(ctx)
			if teamID != 200 || userID != 77 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer sample" {
				t.Errorf("wrong target check: team=%d user=%d auth=%v", teamID, userID, md.Get("authorization"))
			}
			return nil
		},
	}
	expectTaskNotFound(mock)
	expectTaskInsert(mock, nil)
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
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer sample", "idempotency-key", "request-123"))
	result, err := pb.NewTaskClient(conn).CreateTask(ctx, taskRequest())
	if err != nil || result.GetTaskId() <= 0 {
		t.Fatalf("create task RPC: %v, %v", result, err)
	}
}

func TestCreateTaskDuplicateRequest(t *testing.T) {
	for _, tc := range []struct {
		name           string
		previousTitle  string
		previousTeamID int64
		want           codes.Code
	}{
		{"same request", "Planning", 200, codes.OK},
		{"different title", "Other", 200, codes.AlreadyExists},
		{"different team", "Planning", 201, codes.AlreadyExists},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testTaskServer(t)
			s.teamClient = teamCheckerFake{
				checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
					return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
				},
				checkTarget: func(context.Context, int64, int64) error {
					t.Fatal("an existing task must not require current assignee membership")
					return nil
				},
			}
			previousHash, _ := taskRequestFingerprint(tc.previousTeamID, tc.previousTitle, "Check tasks", 77, 0, 0, 0)
			mock.ExpectQuery(regexp.QuoteMeta("SELECT id, request_hash FROM `tasks` WHERE creator_id = ? AND request_key = ?")).
				WithArgs(int64(42), "request-123", 1).
				WillReturnRows(sqlmock.NewRows([]string{"id", "request_hash"}).AddRow(int64(999), previousHash))
			result, err := s.CreateTask(taskContext(), taskRequest())
			if status.Code(err) != tc.want || tc.want == codes.OK && result.GetTaskId() != 999 {
				t.Fatalf("result=%v err=%v, want %v", result, err, tc.want)
			}
		})
	}
}

func TestCreateTaskConcurrentDuplicateUsesSavedRequest(t *testing.T) {
	s, mock := testTaskServer(t)
	expectTaskNotFound(mock)
	expectTaskInsert(mock, &driverMysql.MySQLError{Number: 1062, Message: "duplicate"})
	fingerprint, _ := taskRequestFingerprint(200, "Planning", "Check tasks", 77, 0, 0, 0)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, request_hash FROM `tasks` WHERE creator_id = ? AND request_key = ?")).
		WithArgs(int64(42), "request-123", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "request_hash"}).AddRow(int64(999), fingerprint))
	result, err := s.CreateTask(taskContext(), taskRequest())
	if err != nil || result.GetTaskId() != 999 {
		t.Fatalf("concurrent duplicate: %v, %v", result, err)
	}
}

func TestCreateTaskRejectsInvalidAndUnauthorized(t *testing.T) {
	s, _ := testTaskServer(t)
	for _, req := range []*pb.CreateTaskRequest{nil, {TeamId: 0, Title: "Task"}, {TeamId: 200, Title: " "}, {TeamId: 200, Title: "Task", AssigneeId: -1}, {TeamId: 200, Title: "Task", SourceGroupId: 300}, {TeamId: 200, Title: "Task", SourceMessageId: 400}, {TeamId: 200, Title: "Task", SourceGroupId: -1, SourceMessageId: 400}, {TeamId: 200, Title: "Task", DueAtUnixMs: -1}, {TeamId: 200, Title: "Task", DueAtUnixMs: maxTaskDueAtUnixMs + 1}} {
		result, err := s.CreateTask(taskContext(), req)
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid request: %v, %v", result, err)
		}
	}
	result, err := s.CreateTask(context.Background(), taskRequest())
	if result != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing token: %v, %v", result, err)
	}
	s.teamClient = teamCheckerFake{
		checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "not in team")
		},
		checkTarget: func(context.Context, int64, int64) error { t.Fatal("target check after denied caller"); return nil },
	}
	result, err = s.CreateTask(taskContext(), taskRequest())
	if result != nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-member: %v, %v", result, err)
	}
}

func TestCreateTaskRequiresRequestKey(t *testing.T) {
	s, _ := testTaskServer(t)
	s.teamClient = teamCheckerFake{
		checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
			t.Fatal("invalid key reached team authorization")
			return nil, nil
		},
	}
	for _, keys := range [][]string{nil, {"has space"}, {strings.Repeat("x", 65)}, {"first", "second"}} {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{"authorization": {"Bearer sample"}, "idempotency-key": keys})
		result, err := s.CreateTask(ctx, taskRequest())
		if result != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid keys %v: %v, %v", keys, result, err)
		}
	}
}

func TestCreateTaskWithoutAssignee(t *testing.T) {
	s, mock := testTaskServer(t)
	s.teamClient = teamCheckerFake{
		checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
			return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
		},
		checkTarget: func(context.Context, int64, int64) error {
			t.Fatal("unassigned task must not check a target member")
			return nil
		},
	}
	expectTaskNotFound(mock)
	fingerprint, _ := taskRequestFingerprint(200, "Planning", "Check tasks", 0, 0, 0, 0)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `tasks`")).
		WithArgs(int64(200), "Planning", "Check tasks", int64(42), nil, nil, nil, nil, int8(0), "request-123", fingerprint, positiveTaskID{}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	req := taskRequest()
	req.AssigneeId = 0
	result, err := s.CreateTask(taskContext(), req)
	if err != nil || result.GetTaskId() <= 0 {
		t.Fatalf("unassigned create: %v, %v", result, err)
	}
}

func TestCreateTaskRejectsUnavailableAssigneeAndDatabase(t *testing.T) {
	s, targetMock := testTaskServer(t)
	s.teamClient = teamCheckerFake{
		checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
			return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
		},
		checkTarget: func(context.Context, int64, int64) error { return status.Error(codes.NotFound, "target left team") },
	}
	expectTaskNotFound(targetMock)
	result, err := s.CreateTask(taskContext(), taskRequest())
	if result != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("assignee outside team: %v, %v", result, err)
	}
	s, mock := testTaskServer(t)
	expectTaskNotFound(mock)
	expectTaskInsert(mock, errors.New("database down"))
	result, err = s.CreateTask(taskContext(), taskRequest())
	if result != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("database failure: %v, %v", result, err)
	}
}

func TestCreateTaskWithSourceChecksIMBeforeInsert(t *testing.T) {
	s, mock := testTaskServer(t)
	s.imClient = messageCheckerFake(func(ctx context.Context, req *impb.CheckTeamGroupMessageRequest) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		if req.GetTeamId() != 200 || req.GetGroupId() != 300 || req.GetMessageId() != 400 || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer sample" {
			t.Errorf("wrong IM check: %v metadata=%v", req, md)
		}
		return nil
	})
	expectTaskNotFound(mock)
	fingerprint, _ := taskRequestFingerprint(200, "Planning", "Check tasks", 77, 300, 400, 0)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `tasks`")).
		WithArgs(int64(200), "Planning", "Check tasks", int64(42), int64(77), int64(300), int64(400), nil, int8(0), "request-123", fingerprint, positiveTaskID{}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	req := taskRequest()
	req.SourceGroupId, req.SourceMessageId = 300, 400
	result, err := s.CreateTask(taskContext(), req)
	if err != nil || result.GetTaskId() <= 0 {
		t.Fatalf("create sourced task: %v, %v", result, err)
	}
}

func TestCreateTaskStoresOptionalUTCDueTime(t *testing.T) {
	s, mock := testTaskServer(t)
	const dueAtUnixMs int64 = 1790771400000
	req := taskRequest()
	req.DueAtUnixMs = dueAtUnixMs
	expectTaskNotFound(mock)
	fingerprint, _ := taskRequestFingerprint(200, "Planning", "Check tasks", 77, 0, 0, dueAtUnixMs)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `tasks`")).
		WithArgs(int64(200), "Planning", "Check tasks", int64(42), int64(77), nil, nil, dueAtUnixMs, int8(0), "request-123", fingerprint, positiveTaskID{}).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	result, err := s.CreateTask(taskContext(), req)
	if err != nil || result.GetTaskId() <= 0 {
		t.Fatalf("create task with due time: %v, %v", result, err)
	}
}

func TestTaskDueTimeChangesIdempotencyFingerprintWithoutChangingLegacyHash(t *testing.T) {
	legacy, err := taskRequestFingerprint(200, "Planning", "Check tasks", 77, 0, 0, 0)
	if err != nil || legacy != "6c9e1fd3c47db3fbe165966c2f85ce76c094342c455dc526adde762a1ab0c630" {
		t.Fatalf("legacy fingerprint changed: %q, %v", legacy, err)
	}
	withDue, err := taskRequestFingerprint(200, "Planning", "Check tasks", 77, 0, 0, 1790771400000)
	if err != nil || withDue == legacy {
		t.Fatalf("due time omitted from fingerprint: %q, %v", withDue, err)
	}
}

func TestCreateTaskRejectsInvalidSourceAndDoesNotRecheckDuplicate(t *testing.T) {
	s, mock := testTaskServer(t)
	req := taskRequest()
	req.SourceGroupId, req.SourceMessageId = 300, 400
	expectTaskNotFound(mock)
	result, err := s.CreateTask(taskContext(), req)
	if result != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("missing IM client: %v, %v", result, err)
	}
	s.imClient = messageCheckerFake(func(context.Context, *impb.CheckTeamGroupMessageRequest) error {
		return status.Error(codes.NotFound, "source message not found")
	})
	expectTaskNotFound(mock)
	result, err = s.CreateTask(taskContext(), req)
	if result != nil || status.Code(err) != codes.NotFound {
		t.Fatalf("invalid source: %v, %v", result, err)
	}
	s.imClient = messageCheckerFake(func(context.Context, *impb.CheckTeamGroupMessageRequest) error {
		t.Fatal("idempotent retry must not depend on current source access")
		return nil
	})
	fingerprint, _ := taskRequestFingerprint(200, "Planning", "Check tasks", 77, 300, 400, 0)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, request_hash FROM `tasks` WHERE creator_id = ? AND request_key = ?")).
		WithArgs(int64(42), "request-123", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "request_hash"}).AddRow(int64(999), fingerprint))
	result, err = s.CreateTask(taskContext(), req)
	if err != nil || result.GetTaskId() != 999 {
		t.Fatalf("duplicate sourced task: %v, %v", result, err)
	}
}
