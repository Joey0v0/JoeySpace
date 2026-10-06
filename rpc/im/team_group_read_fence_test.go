package main

import (
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const readFenceTestSQL = "SELECT gm.group_id, f.closed_through_generation FROM group_members AS gm\n" +
	"JOIN `groups` AS g ON g.id = gm.group_id\n" +
	"LEFT JOIN im_team_group_fences AS f ON f.team_id = g.team_id AND f.user_id = gm.user_id\n" +
	"WHERE gm.group_id = ? AND gm.user_id = ? AND g.team_id = ? LIMIT 2"

func expectTeamGroupReadFence(mock sqlmock.Sqlmock, groupID, userID, teamID int64, closed any, present bool) {
	rows := sqlmock.NewRows([]string{"group_id", "closed_through_generation"})
	if present {
		rows.AddRow(groupID, closed)
	}
	mock.ExpectQuery("^"+regexp.QuoteMeta(readFenceTestSQL)+"$").
		WithArgs(groupID, userID, teamID).WillReturnRows(rows)
}

func TestTeamGroupReadFenceFreshScopeAndClosedGeneration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		closed     any
		present    bool
		generation int64
		want       codes.Code
	}{
		{"legacy member before fence initialization", nil, true, 1, codes.OK},
		{"closed old generation", int64(1), true, 1, codes.PermissionDenied},
		{"new generation after closure", int64(1), true, 2, codes.OK},
		{"missing current group member", nil, false, 2, codes.PermissionDenied},
		{"invalid stored closure", int64(-1), true, 2, codes.Unavailable},
		{"large exact closed generation", int64(9007199254740993), true, 9007199254740993, codes.PermissionDenied},
		{"large exact new generation", int64(9007199254740993), true, 9007199254740994, codes.OK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock := testIMServer(t)
			expectTeamGroupReadFence(mock, 300, 42, 200, tc.closed, tc.present)
			err := checkTeamGroupReadGeneration(t.Context(), s.db, 300, 200, 42, tc.generation)
			if status.Code(err) != tc.want {
				t.Fatalf("check: %v, want %v", err, tc.want)
			}
		})
	}
}
