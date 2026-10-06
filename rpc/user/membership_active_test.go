package main

import (
	"context"
	"database/sql/driver"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysql "github.com/go-sql-driver/mysql"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/rpc/user/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNonActiveCallerCannotReadOrManageTeam(t *testing.T) {
	operations := []struct {
		name    string
		query   string
		columns []string
		call    func(*userServer, context.Context) (bool, error)
	}{
		{"qualification", activeTeamMembershipQuery, []string{"user_id", "role", "generation"}, func(s *userServer, ctx context.Context) (bool, error) {
			r, err := s.CheckTeamMember(ctx, &pb.CheckTeamMemberRequest{TeamId: 100})
			return r != nil, err
		}},
		{"group creation authorization", activeTeamMembershipQuery, []string{"user_id", "role", "generation"}, func(s *userServer, ctx context.Context) (bool, error) {
			r, err := s.AuthorizeTeamGroupCreation(ctx, &pb.AuthorizeTeamGroupCreationRequest{TeamId: 100})
			return r != nil, err
		}},
		{"member list", activeListMembershipQuery, []string{"user_id"}, func(s *userServer, ctx context.Context) (bool, error) {
			r, err := s.ListTeamMembers(ctx, &pb.ListTeamMembersRequest{TeamId: 100})
			return r != nil, err
		}},
		{"member target", activeTeamMembershipQuery, []string{"user_id", "role", "generation"}, func(s *userServer, ctx context.Context) (bool, error) {
			r, err := s.CheckTeamMemberByID(ctx, &pb.CheckTeamMemberByIDRequest{TeamId: 100, UserId: 77})
			return r != nil, err
		}},
		{"name candidates", activeTeamMembershipQuery, []string{"user_id", "role", "generation"}, func(s *userServer, ctx context.Context) (bool, error) {
			r, err := s.ResolveTeamMember(ctx, &pb.ResolveTeamMemberRequest{TeamId: 100, Name: "Alice"})
			return r != nil, err
		}},
		{"owner add", activeMemberRoleQuery, []string{"role"}, func(s *userServer, ctx context.Context) (bool, error) {
			r, err := s.AddTeamMember(ctx, &pb.AddTeamMemberRequest{TeamId: 100, UserId: 77})
			return r != nil, err
		}},
		{"owner role change", activeMemberRoleQuery, []string{"role"}, func(s *userServer, ctx context.Context) (bool, error) {
			r, err := s.SetTeamMemberRole(ctx, &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 77, Role: 1})
			return r != nil, err
		}},
	}
	for _, inactive := range []struct {
		name  string
		state int8
	}{{"leaving", model.TeamMembershipLeaving}, {"left", model.TeamMembershipLeft}} {
		for _, op := range operations {
			t.Run(inactive.name+"/"+op.name, func(t *testing.T) {
				s, mock := newTestUserServer(t)
				expectTeamCreator(mock)
				// A stored row in this inactive state is excluded by the exact
				// SQL and bound Active value. sqlmock verifies the predicate; it
				// does not execute a real lifecycle change or MySQL filtering.
				mock.ExpectQuery("^"+regexp.QuoteMeta(op.query)+"$").
					WithArgs(int64(100), int64(42), model.TeamMembershipActive, 1).
					WillReturnRows(sqlmock.NewRows(op.columns))
				// No target/profile/list query, transaction or write may follow.
				result, err := op.call(s, teamContext(t))
				if result || status.Code(err) != codes.PermissionDenied {
					t.Fatalf("state=%d authorized %s: result=%v error=%v", inactive.state, op.name, result, err)
				}
			})
		}
	}
}

func TestTeamQualificationRejectsInvalidIdentityRoleOrGeneration(t *testing.T) {
	for _, ownerAuthorization := range []bool{false, true} {
		for _, row := range []struct {
			name       string
			userID     int64
			role       int8
			generation driver.Value
		}{
			{"zero generation", 42, 2, int64(0)}, {"negative generation", 42, 2, int64(-1)}, {"missing generation", 42, 2, nil},
			{"wrong caller", 77, 2, int64(1)}, {"zero caller", 0, 2, int64(1)},
			{"negative role", 42, -1, int64(1)}, {"unknown role", 42, 3, int64(1)},
		} {
			t.Run(row.name+map[bool]string{false: "/qualification", true: "/owner authorization"}[ownerAuthorization], func(t *testing.T) {
				s, mock := newTestUserServer(t)
				expectTeamCreator(mock)
				mock.ExpectQuery("^"+regexp.QuoteMeta(activeTeamMembershipQuery)+"$").
					WithArgs(int64(100), int64(42), model.TeamMembershipActive, 1).
					WillReturnRows(sqlmock.NewRows([]string{"user_id", "role", "generation"}).AddRow(row.userID, row.role, row.generation))
				var result bool
				var err error
				if ownerAuthorization {
					r, e := s.AuthorizeTeamGroupCreation(teamContext(t), &pb.AuthorizeTeamGroupCreationRequest{TeamId: 100})
					result, err = r != nil, e
				} else {
					r, e := s.CheckTeamMember(teamContext(t), &pb.CheckTeamMemberRequest{TeamId: 100})
					result, err = r != nil, e
				}
				if result || status.Code(err) != codes.Unavailable || status.Convert(err).Message() != "team membership data unavailable" {
					t.Fatalf("invalid qualification granted: result=%v error=%v", result, err)
				}
			})
		}
	}
}

func TestNonActiveTargetCannotBeAssignedOrHaveRoleChanged(t *testing.T) {
	for _, state := range []int8{model.TeamMembershipLeaving, model.TeamMembershipLeft} {
		t.Run(map[int8]string{model.TeamMembershipLeaving: "leaving", model.TeamMembershipLeft: "left"}[state], func(t *testing.T) {
			s, mock := newTestUserServer(t)
			expectTeamCreator(mock)
			expectTeamMembership(mock, true, 0)
			expectTargetTeamMember(mock, nil, nil)
			result, err := s.CheckTeamMemberByID(teamContext(t), &pb.CheckTeamMemberByIDRequest{TeamId: 100, UserId: 77})
			if result != nil || status.Code(err) != codes.NotFound {
				t.Fatalf("inactive target passed qualification: %v %v", result, err)
			}
			expectTeamCreator(mock)
			expectMemberRole(mock, 42, 2)
			mock.ExpectQuery("^"+regexp.QuoteMeta(activeMemberRoleQuery)+"$").WithArgs(int64(100), int64(77), model.TeamMembershipActive, 1).WillReturnRows(sqlmock.NewRows([]string{"role"}))
			changed, err := s.SetTeamMemberRole(teamContext(t), &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 77, Role: 1})
			if changed != nil || status.Code(err) != codes.NotFound {
				t.Fatalf("inactive target allowed role write: %v %v", changed, err)
			}
		})
	}
}

func TestDirectoryAndNameCandidatesExcludeInactiveMembershipRows(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectListMembership(mock)
	mock.ExpectQuery("^"+regexp.QuoteMeta(activeMembersListQuery)+"$").
		WithArgs(int64(100), int64(0), model.TeamMembershipActive, 21).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "username", "nickname", "role"}).AddRow(88, "active-user", "Alice", 0))
	listed, err := s.ListTeamMembers(teamContext(t), &pb.ListTeamMembersRequest{TeamId: 100})
	if err != nil || len(listed.GetMembers()) != 1 || listed.GetMembers()[0].GetUserId() != 88 || listed.GetNextAfterUserId() != 0 {
		t.Fatalf("activity-filtered directory: %v %v", listed, err)
	}
	// Inactive same-name rows cannot create ambiguity or become candidates.
	expectMemberResolution(mock, "Alice").WillReturnRows(resolvedMemberRows().AddRow(88, "active-user", "Alice", 0))
	resolved, err := s.ResolveTeamMember(teamContext(t), &pb.ResolveTeamMemberRequest{TeamId: 100, Name: "Alice"})
	if err != nil || len(resolved.GetCandidates()) != 1 || resolved.GetCandidates()[0].GetUserId() != 88 || resolved.GetTruncated() {
		t.Fatalf("activity-filtered candidates: %v %v", resolved, err)
	}
}

func TestRoleUpdateCannotModifyTargetThatBecameInactive(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectMemberRole(mock, 42, 2)
	expectMemberRole(mock, 77, 0)
	// The lookup was active; the conditional update finds no active row now.
	expectRoleUpdate(mock, 77, 1, 0)
	result, err := s.SetTeamMemberRole(teamContext(t), &pb.SetTeamMemberRoleRequest{TeamId: 100, UserId: 77, Role: 1})
	if result != nil || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("concurrent inactive role update: %v %v", result, err)
	}
}

func TestAddExistingInactiveMemberDoesNotReactivateOrResetGeneration(t *testing.T) {
	s, mock := newTestUserServer(t)
	expectTeamCreator(mock)
	expectOperatorRole(mock, 2)
	expectTargetStatus(mock, 77, 1)
	mock.ExpectBegin()
	// Only the existing three fields are supplied. Database defaults initialize
	// new rows, while the persistent pair key also rejects a leaving/left row.
	mock.ExpectExec("^"+regexp.QuoteMeta("INSERT INTO `team_members` (`team_id`,`user_id`,`role`) VALUES (?,?,?)")+"$").
		WithArgs(int64(100), int64(77), int64(0)).WillReturnError(&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"})
	mock.ExpectRollback()
	result, err := s.AddTeamMember(teamContext(t), &pb.AddTeamMemberRequest{TeamId: 100, UserId: 77})
	if result != nil || status.Code(err) != codes.AlreadyExists {
		t.Fatalf("existing inactive row was reactivated: %v %v", result, err)
	}
}
