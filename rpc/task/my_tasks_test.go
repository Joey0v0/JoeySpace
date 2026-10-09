package main

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/yjydist/go-im/rpc/task/pb"
	userpb "github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const personalMaxSQL = "SELECT MAX(id) AS upper_task_id FROM `tasks` WHERE assignee_id = ? AND team_id IN (?,?)"
const openBucketSQL = "CASE WHEN due_at_unix_ms IS NULL THEN 3 WHEN due_at_unix_ms < 1791500000000 THEN 0 WHEN due_at_unix_ms <= 1792104800000 THEN 1 ELSE 2 END"
const personalOpenSQL = "SELECT id, team_id, title, description, creator_id, assignee_id, status, source_group_id, source_message_id, due_at_unix_ms, " + openBucketSQL + " AS due_bucket FROM `tasks` WHERE (assignee_id = ? AND team_id IN (?,?) AND id <= ?) AND status IN (?,?) ORDER BY due_bucket ASC,due_at_unix_ms ASC,id ASC LIMIT ?"
const personalCompletedSQL = "SELECT id, team_id, title, description, creator_id, assignee_id, status, source_group_id, source_message_id, due_at_unix_ms FROM `tasks` WHERE (assignee_id = ? AND team_id IN (?,?) AND id <= ?) AND status = ? ORDER BY id DESC LIMIT ?"

func expectPersonalMax(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta(personalMaxSQL)).WithArgs(int64(42), int64(200), int64(300)).WillReturnRows(sqlmock.NewRows([]string{"upper_task_id"}).AddRow(personalUpper))
}

func TestListMyTasksValidation(t *testing.T) {
	s, _ := testPersonalServer(t)
	for _, req := range []*pb.ListMyTasksRequest{nil, {View: -1}, {View: 2}, {TeamId: -1}, {Limit: -1}, {Limit: 51}, {Cursor: "!"}, {Cursor: strings.Repeat("a", 2049)}, {Cursor: base64.RawURLEncoding.EncodeToString([]byte("{}{}"))}} {
		if r, err := s.ListMyTasks(taskListContext(), req); r != nil || status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid personal request: %v %v", r, err)
		}
	}
	if _, err := s.ListMyTasks(context.Background(), &pb.ListMyTasksRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	for _, missing := range []string{"db", "team", "identity", "directory"} {
		t.Run(missing, func(t *testing.T) {
			s, _ := testPersonalServer(t)
			switch missing {
			case "db":
				s.db = nil
			case "team":
				s.teamClient = nil
			case "identity":
				s.identityClient = nil
			case "directory":
				s.teamDirectory = nil
			}
			if _, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{}); status.Code(err) != codes.Unavailable {
				t.Fatal(err)
			}
		})
	}
}

func TestListMyTasksOpenPageAndContinuity(t *testing.T) {
	s, mock := testPersonalServer(t)
	expectPersonalMax(mock)
	columns := append(append([]string{}, personalColumns...), "due_bucket")
	mock.ExpectQuery(regexp.QuoteMeta(personalOpenSQL)).WithArgs(int64(42), int64(200), int64(300), personalUpper, 0, 1, 3).WillReturnRows(sqlmock.NewRows(columns).
		AddRow(int64(9007199254740993), 200, "overdue", "d", 8, 42, 0, 5, 6, personalNow-1, 0).
		AddRow(int64(9007199254740994), 300, "now", "d", 8, 42, 1, nil, nil, personalNow, 1).
		AddRow(int64(9007199254740995), 200, "week", "d", 8, 42, 0, nil, nil, personalNow+604800000, 1))
	r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{Limit: 2})
	if err != nil || len(r.GetTasks()) != 2 || r.GetTasks()[0].GetTaskId() != 9007199254740993 || r.GetTasks()[0].GetTeamId() != 200 || r.GetTasks()[0].GetSourceMessageId() != 6 || r.GetTasks()[1].GetTeamId() != 300 || r.GetNextCursor() == "" {
		t.Fatalf("first personal page: %v %v", r, err)
	}
	firstCursor := r.GetNextCursor()
	// The next request must keep the first snapshot even when the clock changes.
	s.now = nil
	keysetSQL := strings.Replace(personalOpenSQL, " ORDER BY", " AND (("+openBucketSQL+" > ? OR ("+openBucketSQL+" = ? AND (due_at_unix_ms > ? OR (due_at_unix_ms = ? AND id > ?))))) ORDER BY", 1)
	mock.ExpectQuery(regexp.QuoteMeta(keysetSQL)).WithArgs(int64(42), int64(200), int64(300), personalUpper, 0, 1, 1, 1, personalNow, personalNow, int64(9007199254740994), 4).WillReturnRows(sqlmock.NewRows(columns).
		AddRow(int64(9007199254740995), 200, "week", "d", 8, 42, 0, nil, nil, personalNow+604800000, 1).
		AddRow(int64(9007199254740996), 200, "later", "d", 8, 42, 0, nil, nil, personalNow+604800001, 2).
		AddRow(int64(9007199254740997), 300, "undated", "d", 8, 42, 1, nil, nil, nil, 3))
	r, err = s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{Limit: 3, Cursor: firstCursor})
	if err != nil || len(r.GetTasks()) != 3 || r.GetTasks()[0].GetTaskId() != 9007199254740995 || r.GetTasks()[2].GetDueAtUnixMs() != 0 || r.GetNextCursor() != "" {
		t.Fatalf("continuity: %v %v", r, err)
	}
	for _, req := range []*pb.ListMyTasksRequest{{View: 1, Cursor: firstCursor}, {TeamId: 200, Cursor: firstCursor}} {
		if _, err := s.ListMyTasks(taskListContext(), req); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("reused cursor: %v", err)
		}
	}
}

func TestListMyTasksCompletedDepartureAndNullContinuation(t *testing.T) {
	s, mock := testPersonalServer(t)
	expectPersonalMax(mock)
	mock.ExpectQuery(regexp.QuoteMeta(personalCompletedSQL)).WithArgs(int64(42), int64(200), int64(300), personalUpper, 2, 2).WillReturnRows(sqlmock.NewRows(personalColumns).
		AddRow(int64(9), 200, "new", "", 8, 42, 2, nil, nil, nil).
		AddRow(int64(8), 300, "old", "", 8, 42, 2, nil, nil, nil))
	r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{View: 1, Limit: 1})
	if err != nil || len(r.GetTasks()) != 1 || r.GetTasks()[0].GetTaskId() != 9 || r.GetNextCursor() == "" {
		t.Fatalf("completed: %v %v", r, err)
	}
	s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 300}}}, nil
	})
	query := strings.Replace(personalCompletedSQL, "IN (?,?)", "IN (?)", 1)
	query = strings.Replace(query, " ORDER BY", " AND id < ? ORDER BY", 1)
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(int64(42), int64(300), personalUpper, 2, int64(9), 2).WillReturnRows(sqlmock.NewRows(personalColumns).AddRow(int64(8), 300, "old", "", 8, 42, 2, nil, nil, nil))
	next, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{View: 1, Limit: 1, Cursor: r.GetNextCursor()})
	if err != nil || len(next.GetTasks()) != 1 || next.GetTasks()[0].GetTaskId() != 8 || next.GetNextCursor() != "" {
		t.Fatalf("departure page: %v %v", next, err)
	}
	// NULL-due continuation compares IDs only, never NULL equality.
	cursor := base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"view":0,"team_id":0,"snapshot_now_unix_ms":1791500000000,"snapshot_upper_task_id":9007199254740999,"last_bucket":3,"last_due_at_unix_ms":0,"last_task_id":8,"last_status":1}`))
	query = strings.Replace(personalOpenSQL, "IN (?,?)", "IN (?)", 1)
	query = strings.Replace(query, " ORDER BY", " AND (("+openBucketSQL+" = ? AND id > ?)) ORDER BY", 1)
	mock.ExpectQuery(regexp.QuoteMeta(query)).WithArgs(int64(42), int64(300), personalUpper, 0, 1, 3, int64(8), 21).WillReturnRows(sqlmock.NewRows(append(append([]string{}, personalColumns...), "due_bucket")).AddRow(int64(10), 300, "last", "", 8, 42, 1, nil, nil, nil, 3))
	last, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{Cursor: cursor})
	if err != nil || len(last.GetTasks()) != 1 || last.GetTasks()[0].GetTaskId() != 10 {
		t.Fatalf("undated continuation: %v %v", last, err)
	}
}

func TestListMyTasksDirectoryPagesAndEmpty(t *testing.T) {
	s, mock := testPersonalServer(t)
	calls := 0
	s.teamDirectory = taskDirectoryFake(func(ctx context.Context, req *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		requirePersonalBearer(t, ctx)
		calls++
		if req.GetLimit() != 100 {
			t.Fatal(req)
		}
		if calls == 1 && req.GetAfterTeamId() == 0 {
			return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 200}}, NextAfterTeamId: 200}, nil
		}
		if calls == 2 && req.GetAfterTeamId() == 200 {
			return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 300}}}, nil
		}
		t.Fatal(req)
		return nil, nil
	})
	expectPersonalMax(mock)
	mock.ExpectQuery(regexp.QuoteMeta(personalOpenSQL)).WithArgs(int64(42), int64(200), int64(300), personalUpper, 0, 1, 51).WillReturnRows(sqlmock.NewRows(append(append([]string{}, personalColumns...), "due_bucket")))
	if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{Limit: 50}); err != nil || len(r.GetTasks()) != 0 || r.GetNextCursor() != "" {
		t.Fatalf("paged teams: %v %v", r, err)
	}
	s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
		return &userpb.ListMyTeamsResponse{}, nil
	})
	if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{}); err != nil || len(r.GetTasks()) != 0 {
		t.Fatalf("empty teams: %v %v", r, err)
	}
}

func TestListMyTasksExplicitTeamAndFailures(t *testing.T) {
	t.Run("explicit", func(t *testing.T) {
		s, mock := testPersonalServer(t)
		s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
			t.Fatal("explicit team must not list directory")
			return nil, nil
		})
		s.teamClient = teamCheckerFake{checkMember: func(ctx context.Context, id int64) (*userpb.CheckTeamMemberResponse, error) {
			requirePersonalBearer(t, ctx)
			if id != 200 {
				t.Fatal(id)
			}
			return &userpb.CheckTeamMemberResponse{UserId: 42}, nil
		}}
		mock.ExpectQuery(regexp.QuoteMeta(strings.Replace(personalMaxSQL, "IN (?,?)", "IN (?)", 1))).WithArgs(int64(42), int64(200)).WillReturnRows(sqlmock.NewRows([]string{"upper_task_id"}).AddRow(nil))
		if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{TeamId: 200}); err != nil || len(r.GetTasks()) != 0 {
			t.Fatalf("empty filter: %v %v", r, err)
		}
		s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "left")
		}}
		if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{TeamId: 200}); r != nil || status.Code(err) != codes.PermissionDenied {
			t.Fatalf("filtered departure: %v %v", r, err)
		}
	})
	t.Run("identity", func(t *testing.T) {
		s, _ := testPersonalServer(t)
		s.identityClient = taskIdentityFake(func(context.Context) (*userpb.GetUserInfoResponse, error) {
			return nil, status.Error(codes.Unauthenticated, "expired")
		})
		if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{}); r != nil || status.Code(err) != codes.Unauthenticated {
			t.Fatalf("identity: %v %v", r, err)
		}
	})
	for _, stage := range []string{"max", "rows"} {
		t.Run(stage, func(t *testing.T) {
			s, mock := testPersonalServer(t)
			if stage == "max" {
				mock.ExpectQuery(regexp.QuoteMeta(personalMaxSQL)).WillReturnError(errors.New("private DB"))
			} else {
				expectPersonalMax(mock)
				mock.ExpectQuery(regexp.QuoteMeta(personalOpenSQL)).WillReturnError(errors.New("private DB"))
			}
			if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{}); r != nil || status.Code(err) != codes.Unavailable {
				t.Fatalf("DB: %v %v", r, err)
			}
		})
	}
}

func TestListMyTasksRejectsMalformedDirectoryAndPartialFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		page *userpb.ListMyTeamsResponse
	}{
		{"nil", nil}, {"nil item", &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{nil}}}, {"zero ID", &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{}}}},
		{"negative ID", &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: -1}}}}, {"duplicate", &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 200}, {TeamId: 200}}}},
		{"unordered", &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 300}, {TeamId: 200}}}}, {"negative cursor", &userpb.ListMyTeamsResponse{NextAfterTeamId: -1}}, {"empty nonterminal", &userpb.ListMyTeamsResponse{NextAfterTeamId: 200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testPersonalServer(t)
			s.teamDirectory = taskDirectoryFake(func(context.Context, *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
				return tc.page, nil
			})
			if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{}); r != nil || status.Code(err) != codes.Unavailable {
				t.Fatalf("bad directory: %v %v", r, err)
			}
		})
	}
	for _, mode := range []string{"failure", "stuck cursor", "duplicate across pages"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testPersonalServer(t)
			s.teamDirectory = taskDirectoryFake(func(_ context.Context, req *userpb.ListMyTeamsRequest) (*userpb.ListMyTeamsResponse, error) {
				if req.GetAfterTeamId() == 0 {
					return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 200}}, NextAfterTeamId: 200}, nil
				}
				switch mode {
				case "failure":
					return nil, errors.New("private User error")
				case "stuck cursor":
					return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 300}}, NextAfterTeamId: 200}, nil
				default:
					return &userpb.ListMyTeamsResponse{Teams: []*userpb.MyTeam{{TeamId: 200}}}, nil
				}
			})
			if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{}); r != nil || status.Code(err) != codes.Unavailable {
				t.Fatalf("partial: %v %v", r, err)
			}
		})
	}
}

func TestListMyTasksNullDueNextCursorClearsPreviousDue(t *testing.T) {
	s, mock := testPersonalServer(t)
	cursor := base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"view":0,"team_id":0,"snapshot_now_unix_ms":1791500000000,"snapshot_upper_task_id":9007199254740999,"last_bucket":1,"last_due_at_unix_ms":1791500000000,"last_task_id":7,"last_status":0}`))
	columns := append(append([]string{}, personalColumns...), "due_bucket")
	keyset := strings.Replace(personalOpenSQL, " ORDER BY", " AND (("+openBucketSQL+" > ? OR ("+openBucketSQL+" = ? AND (due_at_unix_ms > ? OR (due_at_unix_ms = ? AND id > ?))))) ORDER BY", 1)
	mock.ExpectQuery(regexp.QuoteMeta(keyset)).WithArgs(int64(42), int64(200), int64(300), personalUpper, 0, 1, 1, 1, personalNow, personalNow, int64(7), 2).WillReturnRows(sqlmock.NewRows(columns).
		AddRow(int64(8), 200, "undated A", "", 9, 42, 0, nil, nil, nil, 3).
		AddRow(int64(9), 300, "undated B", "", 9, 42, 0, nil, nil, nil, 3))
	r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{Cursor: cursor, Limit: 1})
	if err != nil || len(r.GetTasks()) != 1 || r.GetNextCursor() == "" {
		t.Fatalf("undated page: %v %v", r, err)
	}
	// Cursor must remain usable when the page crosses into the NULL bucket.
	keyset = strings.Replace(personalOpenSQL, " ORDER BY", " AND (("+openBucketSQL+" = ? AND id > ?)) ORDER BY", 1)
	mock.ExpectQuery(regexp.QuoteMeta(keyset)).WithArgs(int64(42), int64(200), int64(300), personalUpper, 0, 1, 3, int64(8), 2).WillReturnRows(sqlmock.NewRows(columns).AddRow(int64(9), 300, "undated B", "", 9, 42, 0, nil, nil, nil, 3))
	next, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{Cursor: r.GetNextCursor(), Limit: 1})
	if err != nil || len(next.GetTasks()) != 1 || next.GetTasks()[0].GetTaskId() != 9 || next.GetNextCursor() != "" {
		t.Fatalf("undated next cursor: %v %v", next, err)
	}
}

func TestListMyTasksRejectsMissingIdentityAndMismatchedTeamIdentity(t *testing.T) {
	for _, caller := range []*userpb.GetUserInfoResponse{nil, {}, {Id: -1}} {
		s, _ := testPersonalServer(t)
		s.identityClient = taskIdentityFake(func(context.Context) (*userpb.GetUserInfoResponse, error) { return caller, nil })
		if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{}); r != nil || status.Code(err) != codes.Unavailable {
			t.Fatalf("bad identity: %v %v", r, err)
		}
	}
	s, _ := testPersonalServer(t)
	s.teamClient = teamCheckerFake{checkMember: func(context.Context, int64) (*userpb.CheckTeamMemberResponse, error) {
		return &userpb.CheckTeamMemberResponse{UserId: 7}, nil
	}}
	if r, err := s.ListMyTasks(taskListContext(), &pb.ListMyTasksRequest{TeamId: 200}); r != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("identity mismatch: %v %v", r, err)
	}
}
