package main

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type joinMembershipResponseFunc struct {
	teamCheckFunc
	check func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error)
}

func (f joinMembershipResponseFunc) CheckTeamMember(ctx context.Context, req *userpb.CheckTeamMemberRequest, _ ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	return f.check(ctx, req)
}

func joinGenerationContext(t *testing.T) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+validIMToken(t)))
}

func joinGenerationClient(generation int64) joinMembershipResponseFunc {
	return joinMembershipResponseFunc{check: func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
		return &userpb.CheckTeamMemberResponse{UserId: 42, Generation: generation}, nil
	}}
}

func expectJoinGroupScope(mock sqlmock.Sqlmock) {
	mock.ExpectQuery("^"+regexp.QuoteMeta("SELECT `id` FROM `groups` WHERE id = ? AND team_id = ? LIMIT ?")+"$").
		WithArgs(int64(300), int64(200), 1).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(300)))
}

const joinMemberInsertSQL = "INSERT INTO `group_members` (`group_id`,`user_id`,`role`) VALUES (?,?,?)"
const joinExistingMemberSQL = "SELECT `group_id` FROM `group_members` WHERE group_id = ? AND user_id = ? LIMIT ?"

func TestJoinTeamGroupRejectsInvalidUserAuthorizationBeforeDatabase(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *userpb.CheckTeamMemberResponse
	}{
		{"nil", nil},
		{"missing fields", &userpb.CheckTeamMemberResponse{}},
		{"missing user", &userpb.CheckTeamMemberResponse{Generation: 1}},
		{"wrong user", &userpb.CheckTeamMemberResponse{UserId: 77, Generation: 1}},
		{"missing generation", &userpb.CheckTeamMemberResponse{UserId: 42}},
		{"negative generation", &userpb.CheckTeamMemberResponse{UserId: 42, Generation: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testIMServer(t)
			s.teamClient = joinMembershipResponseFunc{check: func(context.Context, *userpb.CheckTeamMemberRequest) (*userpb.CheckTeamMemberResponse, error) {
				return tc.response, nil
			}}
			// Exact authorization error also distinguishes an unexpected database call.
			result, err := s.JoinTeamGroup(joinGenerationContext(t), &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
			if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() != "team membership authorization is invalid" {
				t.Fatalf("invalid authorization: result=%v error=%v", result, err)
			}
		})
	}
}

func TestJoinTeamGroupClosedGenerationPrecedesAnyMemberWriteOrDuplicateSuccess(t *testing.T) {
	for _, tc := range []struct{ generation, closed int64 }{
		{1, 3}, {3, 3}, {9007199254740993, 9007199254740993}, {math.MaxInt64, math.MaxInt64},
	} {
		t.Run(strconv.FormatInt(tc.generation, 10), func(t *testing.T) {
			s, mock := testIMServer(t)
			s.teamClient = joinGenerationClient(tc.generation)
			expectJoinGroupScope(mock)
			mock.ExpectBegin()
			fenceTestLock(mock, tc.closed)
			mock.ExpectRollback()
			// No INSERT or member lookup: even an existing member cannot bypass closure.
			result, err := s.JoinTeamGroup(joinGenerationContext(t), &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
			if result != nil || status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "team group membership generation is closed" {
				t.Fatalf("closed generation: result=%v error=%v", result, err)
			}
		})
	}
}

func TestJoinTeamGroupExplicitCurrentGenerationWritesAfterClosure(t *testing.T) {
	for _, tc := range []struct{ generation, closed int64 }{
		{1, 0}, {4, 3}, {9007199254740993, 9007199254740992}, {math.MaxInt64, math.MaxInt64 - 1},
	} {
		t.Run(strconv.FormatInt(tc.generation, 10), func(t *testing.T) {
			s, mock := testIMServer(t)
			s.teamClient = joinGenerationClient(tc.generation)
			expectJoinGroupScope(mock)
			mock.ExpectBegin()
			fenceTestLock(mock, tc.closed)
			mock.ExpectExec("^"+regexp.QuoteMeta(joinMemberInsertSQL)+"$").
				WithArgs(int64(300), int64(42), int8(0)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			result, err := s.JoinTeamGroup(joinGenerationContext(t), &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
			if result == nil || err != nil {
				t.Fatalf("explicit current generation: result=%v error=%v", result, err)
			}
		})
	}
}

func TestJoinTeamGroupDuplicateVerificationKeepsGenerationLockUntilCommit(t *testing.T) {
	s, mock := testIMServer(t)
	s.teamClient = joinGenerationClient(4)
	expectJoinGroupScope(mock)
	mock.ExpectBegin()
	fenceTestLock(mock, 3)
	mock.ExpectExec("^"+regexp.QuoteMeta(joinMemberInsertSQL)+"$").
		WithArgs(int64(300), int64(42), int8(0)).WillReturnError(&mysql.MySQLError{Number: 1062})
	// sqlmock's ordered expectations reject releasing the transaction before this query.
	mock.ExpectQuery("^"+regexp.QuoteMeta(joinExistingMemberSQL)+"$").
		WithArgs(int64(300), int64(42), 1).
		WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(300)))
	mock.ExpectCommit()
	result, err := s.JoinTeamGroup(joinGenerationContext(t), &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
	if result == nil || err != nil {
		t.Fatalf("duplicate within generation lock: result=%v error=%v", result, err)
	}
}

func TestJoinTeamGroupWriteAndDuplicateFailuresNeverSucceed(t *testing.T) {
	for _, stage := range []string{"begin", "initialize", "lock", "insert", "duplicate missing", "duplicate database", "duplicate wrong group", "duplicate zero group", "commit", "duplicate commit"} {
		t.Run(stage, func(t *testing.T) {
			s, mock := testIMServer(t)
			s.teamClient = joinGenerationClient(4)
			expectJoinGroupScope(mock)
			privateErr := errors.New("private database detail")
			if stage == "begin" {
				mock.ExpectBegin().WillReturnError(privateErr)
			} else {
				mock.ExpectBegin()
				switch stage {
				case "initialize":
					mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestInitSQL)+"$").WithArgs(int64(200), int64(42)).WillReturnError(privateErr)
				case "lock":
					mock.ExpectExec("^"+regexp.QuoteMeta(fenceTestInitSQL)+"$").WithArgs(int64(200), int64(42)).WillReturnResult(sqlmock.NewResult(0, 0))
					mock.ExpectQuery("^"+regexp.QuoteMeta(fenceTestLockSQL)+"$").WithArgs(int64(200), int64(42)).WillReturnError(privateErr)
				default:
					fenceTestLock(mock, 3)
					insert := mock.ExpectExec("^"+regexp.QuoteMeta(joinMemberInsertSQL)+"$").WithArgs(int64(300), int64(42), int8(0))
					switch stage {
					case "insert":
						insert.WillReturnError(privateErr)
					case "commit":
						insert.WillReturnResult(sqlmock.NewResult(0, 1))
					default:
						insert.WillReturnError(&mysql.MySQLError{Number: 1062})
						lookup := mock.ExpectQuery("^"+regexp.QuoteMeta(joinExistingMemberSQL)+"$").WithArgs(int64(300), int64(42), 1)
						switch stage {
						case "duplicate database":
							lookup.WillReturnError(privateErr)
						case "duplicate wrong group":
							lookup.WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(301)))
						case "duplicate zero group":
							lookup.WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(0)))
						case "duplicate commit":
							lookup.WillReturnRows(sqlmock.NewRows([]string{"group_id"}).AddRow(int64(300)))
						default:
							lookup.WillReturnRows(sqlmock.NewRows([]string{"group_id"}))
						}
					}
				}
				if stage == "commit" || stage == "duplicate commit" {
					mock.ExpectCommit().WillReturnError(privateErr)
				} else {
					mock.ExpectRollback()
				}
			}
			result, err := s.JoinTeamGroup(joinGenerationContext(t), &pb.JoinTeamGroupRequest{TeamId: 200, GroupId: 300})
			if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() != "IM database unavailable" {
				t.Fatalf("%s: result=%v error=%v", stage, result, err)
			}
		})
	}
}
