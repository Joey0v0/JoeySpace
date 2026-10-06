package main

import (
	"context"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/rpc/im/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type createAuthorizationResponseFunc func(context.Context, *userpb.AuthorizeTeamGroupCreationRequest) (*userpb.AuthorizeTeamGroupCreationResponse, error)

func (f createAuthorizationResponseFunc) AuthorizeTeamGroupCreation(ctx context.Context, req *userpb.AuthorizeTeamGroupCreationRequest, _ ...grpc.CallOption) (*userpb.AuthorizeTeamGroupCreationResponse, error) {
	return f(ctx, req)
}

func (createAuthorizationResponseFunc) CheckTeamMember(context.Context, *userpb.CheckTeamMemberRequest, ...grpc.CallOption) (*userpb.CheckTeamMemberResponse, error) {
	return nil, status.Error(codes.Unimplemented, "not used by creation generation tests")
}

func createGenerationContext(t *testing.T) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(
		"authorization", "Bearer "+validIMToken(t), "idempotency-key", "request-123"))
}

func createGenerationClient(generation int64) createAuthorizationResponseFunc {
	return func(context.Context, *userpb.AuthorizeTeamGroupCreationRequest) (*userpb.AuthorizeTeamGroupCreationResponse, error) {
		return &userpb.AuthorizeTeamGroupCreationResponse{UserId: 42, Generation: generation}, nil
	}
}

func expectCreateGenerationGroup(mock sqlmock.Sqlmock) *sqlmock.ExpectedExec {
	return mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `groups`")).
		WithArgs("Planning", int64(42), int64(200), "request-123", imPositiveID{})
}

func expectCreateGenerationOwner(mock sqlmock.Sqlmock) *sqlmock.ExpectedExec {
	return mock.ExpectExec(regexp.QuoteMeta("INSERT INTO `group_members`")).
		WithArgs(imPositiveID{}, int64(42), int8(2))
}

func expectCreateGenerationReplay(mock sqlmock.Sqlmock, previousID int64) {
	expectCreateGenerationGroup(mock).WillReturnError(&mysql.MySQLError{Number: 1062, Message: "private group duplicate"})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, team_id, name FROM `groups` WHERE owner_id = ? AND request_key = ?")).
		WithArgs(int64(42), "request-123", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "team_id", "name"}).AddRow(previousID, int64(200), "Planning"))
}

// Observe attempted queries too: sqlmock returning an error for an unexpected
// replay lookup must not hide a bug behind the expected Unavailable response.
type createGenerationSQLRecorder struct {
	logger.Interface
	statements []string
}

func (r *createGenerationSQLRecorder) Trace(_ context.Context, _ time.Time, statement func() (string, int64), _ error) {
	sql, _ := statement()
	r.statements = append(r.statements, sql)
}

func TestCreateTeamGroupRejectsInvalidAuthorizationResponseBeforeTransaction(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *userpb.AuthorizeTeamGroupCreationResponse
	}{
		{"nil", nil},
		{"missing fields", &userpb.AuthorizeTeamGroupCreationResponse{}},
		{"missing user", &userpb.AuthorizeTeamGroupCreationResponse{Generation: 1}},
		{"wrong user", &userpb.AuthorizeTeamGroupCreationResponse{UserId: 43, Generation: 1}},
		{"zero generation", &userpb.AuthorizeTeamGroupCreationResponse{UserId: 42}},
		{"negative generation", &userpb.AuthorizeTeamGroupCreationResponse{UserId: 42, Generation: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := teamGroupServer(t)
			s.teamClient = createAuthorizationResponseFunc(func(context.Context, *userpb.AuthorizeTeamGroupCreationRequest) (*userpb.AuthorizeTeamGroupCreationResponse, error) {
				return tc.response, nil
			})
			result, err := s.CreateTeamGroup(createGenerationContext(t), &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
			// Check the validation message as well as the code: an unexpected
			// SQL attempt would otherwise also be sanitized as Unavailable.
			if result != nil || status.Code(err) != codes.Unavailable || status.Convert(err).Message() != "team membership authorization is invalid" {
				t.Fatalf("invalid User response returned %v, %v", result, err)
			}
		})
	}
}

func TestCreateTeamGroupNewGenerationWritesGroupAndOwnerAfterAuthorization(t *testing.T) {
	for _, generation := range []int64{4, 9007199254740993, math.MaxInt64} {
		t.Run(strconv.FormatInt(generation, 10), func(t *testing.T) {
			s, mock := teamGroupServer(t)
			ctx := createGenerationContext(t)
			incoming, _ := metadata.FromIncomingContext(ctx)
			calls := 0
			s.teamClient = createAuthorizationResponseFunc(func(ctx context.Context, req *userpb.AuthorizeTeamGroupCreationRequest) (*userpb.AuthorizeTeamGroupCreationResponse, error) {
				calls++
				outgoing, _ := metadata.FromOutgoingContext(ctx)
				if req.GetTeamId() != 200 || len(outgoing.Get("authorization")) != 1 || outgoing.Get("authorization")[0] != incoming.Get("authorization")[0] {
					t.Fatalf("authorization scope/header changed: %v, %v", req, outgoing)
				}
				// Installing expectations here ensures User succeeds before the
				// transaction and its generation lock can begin.
				mock.ExpectBegin()
				fenceTestLock(mock, generation-1)
				expectCreateGenerationGroup(mock).WillReturnResult(sqlmock.NewResult(0, 1))
				expectCreateGenerationOwner(mock).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
				return &userpb.AuthorizeTeamGroupCreationResponse{UserId: 42, Generation: generation}, nil
			})
			result, err := s.CreateTeamGroup(ctx, &pb.CreateTeamGroupRequest{TeamId: 200, Name: " Planning "})
			if err != nil || result.GetGroupId() <= 0 || calls != 1 {
				t.Fatalf("generation %d: result %v, error %v, User calls %d", generation, result, err, calls)
			}
		})
	}
}

func TestCreateTeamGroupClosedGenerationRejectsBeforeAnyGroupWrite(t *testing.T) {
	for _, tc := range []struct{ closed, generation int64 }{{3, 1}, {3, 3}, {9007199254740993, 9007199254740993}, {math.MaxInt64, math.MaxInt64}} {
		t.Run(strconv.FormatInt(tc.closed, 10)+"/"+strconv.FormatInt(tc.generation, 10), func(t *testing.T) {
			s, mock := teamGroupServer(t)
			s.teamClient = createGenerationClient(tc.generation)
			mock.ExpectBegin()
			fenceTestLock(mock, tc.closed)
			mock.ExpectRollback()
			result, err := s.CreateTeamGroup(createGenerationContext(t), &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
			if result != nil || status.Code(err) != codes.PermissionDenied {
				t.Fatalf("closed=%d, generation=%d returned %v, %v", tc.closed, tc.generation, result, err)
			}
		})
	}
}

func TestCreateTeamGroupReplayHonorsClosureAndNeverRestoresOwner(t *testing.T) {
	s, mock := teamGroupServer(t)
	ctx := createGenerationContext(t)
	const originalGeneration int64 = 9007199254740993
	const previousID int64 = 9007199254741001
	for _, tc := range []struct {
		closed, generation int64
		want               codes.Code
	}{{0, originalGeneration, codes.OK}, {originalGeneration, originalGeneration, codes.PermissionDenied}, {originalGeneration, originalGeneration + 1, codes.OK}} {
		s.teamClient = createGenerationClient(tc.generation)
		mock.ExpectBegin()
		fenceTestLock(mock, tc.closed)
		if tc.want == codes.OK {
			expectCreateGenerationReplay(mock, previousID)
			// No owner INSERT is expected, including after a newer generation
			// was authorized. A creation replay is not a fresh explicit Join.
			mock.ExpectCommit()
		} else {
			// The old request key cannot bypass the closed generation check.
			mock.ExpectRollback()
		}
		result, err := s.CreateTeamGroup(ctx, &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
		if status.Code(err) != tc.want || tc.want == codes.OK && result.GetGroupId() != previousID || tc.want != codes.OK && result != nil {
			t.Fatalf("closed=%d, generation=%d replay returned %v, %v", tc.closed, tc.generation, result, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCreateTeamGroupTransactionFailuresNeverReturnSuccess(t *testing.T) {
	for _, stage := range []string{"group insert", "owner duplicate", "commit", "replay commit", "replay lookup", "replay missing", "replay invalid ID"} {
		t.Run(stage, func(t *testing.T) {
			s, mock := teamGroupServer(t)
			recorder := &createGenerationSQLRecorder{Interface: logger.Default.LogMode(logger.Silent)}
			s.db = s.db.Session(&gorm.Session{Logger: recorder})
			s.teamClient = createGenerationClient(4)
			mock.ExpectBegin()
			fenceTestLock(mock, 3)
			privateErr := errors.New("private SQL credentials")
			switch stage {
			case "group insert":
				expectCreateGenerationGroup(mock).WillReturnError(privateErr)
			case "owner duplicate":
				expectCreateGenerationGroup(mock).WillReturnResult(sqlmock.NewResult(0, 1))
				// A group_members 1062 cannot trigger a groups replay lookup.
				expectCreateGenerationOwner(mock).WillReturnError(&mysql.MySQLError{Number: 1062, Message: "private owner duplicate"})
			case "commit":
				expectCreateGenerationGroup(mock).WillReturnResult(sqlmock.NewResult(0, 1))
				expectCreateGenerationOwner(mock).WillReturnResult(sqlmock.NewResult(0, 1))
			case "replay commit":
				expectCreateGenerationReplay(mock, 999)
			default:
				expectCreateGenerationGroup(mock).WillReturnError(&mysql.MySQLError{Number: 1062, Message: "private group duplicate"})
				lookup := mock.ExpectQuery(regexp.QuoteMeta("SELECT id, team_id, name FROM `groups` WHERE owner_id = ? AND request_key = ?")).
					WithArgs(int64(42), "request-123", 1)
				if stage == "replay lookup" {
					lookup.WillReturnError(privateErr)
				} else {
					rows := sqlmock.NewRows([]string{"id", "team_id", "name"})
					if stage == "replay invalid ID" {
						rows.AddRow(int64(0), int64(200), "Planning")
					}
					lookup.WillReturnRows(rows)
				}
			}
			if stage == "commit" || stage == "replay commit" {
				mock.ExpectCommit().WillReturnError(privateErr)
			} else {
				mock.ExpectRollback()
			}
			result, err := s.CreateTeamGroup(createGenerationContext(t), &pb.CreateTeamGroupRequest{TeamId: 200, Name: "Planning"})
			if result != nil || status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "private") {
				t.Fatalf("stage %s returned %v, %v", stage, result, err)
			}
			if stage == "owner duplicate" || stage == "group insert" {
				for _, sql := range recorder.statements {
					if strings.HasPrefix(sql, "SELECT id, team_id, name FROM `groups`") {
						t.Fatalf("stage %s attempted creation replay lookup: %s", stage, sql)
					}
				}
			}
		})
	}
}
