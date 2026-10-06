package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const deliveryFenceQuery = "SELECT gm.group_id, f.closed_through_generation FROM group_members AS gm JOIN `groups` AS g ON g.id = gm.group_id LEFT JOIN im_team_group_fences AS f ON f.team_id = g.team_id AND f.user_id = gm.user_id WHERE gm.group_id = ? AND gm.user_id = ? AND g.team_id = ? LIMIT 2"

func TestCheckTeamGroupMemberGenerationUsesCurrentIMRows(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rows     *sqlmock.Rows
		queryErr error
		allow    bool
		fail     bool
	}{
		{"no fence yet", sqlmock.NewRows([]string{"group_id", "closed_through_generation"}).AddRow(300, nil), nil, true, false},
		{"open generation", sqlmock.NewRows([]string{"group_id", "closed_through_generation"}).AddRow(300, 6), nil, true, false},
		{"closed generation", sqlmock.NewRows([]string{"group_id", "closed_through_generation"}).AddRow(300, 7), nil, false, false},
		{"removed member", sqlmock.NewRows([]string{"group_id", "closed_through_generation"}), nil, false, false},
		{"bad fence", sqlmock.NewRows([]string{"group_id", "closed_through_generation"}).AddRow(300, -1), nil, false, true},
		{"duplicate", sqlmock.NewRows([]string{"group_id", "closed_through_generation"}).AddRow(300, 0).AddRow(300, 0), nil, false, true},
		{"database failure", nil, errors.New("private SQL"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			query := mock.ExpectQuery(regexp.QuoteMeta(deliveryFenceQuery)).WithArgs(int64(300), int64(42), int64(100))
			if tc.queryErr != nil {
				query.WillReturnError(tc.queryErr)
			} else {
				query.WillReturnRows(tc.rows)
			}
			allow, err := (&groupRepository{db: db}).CheckTeamGroupMemberGeneration(context.Background(), 300, 100, 42, 7)
			if allow != tc.allow || (err != nil) != tc.fail {
				t.Fatalf("allow=%t err=%v", allow, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestListMyGroupsOnlyQueriesLegacyGroups(t *testing.T) {
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
	mock.ExpectQuery(regexp.QuoteMeta("FROM `groups` JOIN group_members ON group_members.group_id = groups.id WHERE group_members.user_id = ? AND groups.team_id IS NULL")).
		WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "owner_id", "team_id"}).AddRow(int64(100), "legacy", int64(42), nil))
	groups, err := (&groupRepository{db: db}).ListMyGroups(context.Background(), 42)
	if err != nil || len(groups) != 1 || groups[0].ID != 100 || groups[0].TeamID != nil {
		t.Fatalf("legacy groups: %+v, %v", groups, err)
	}
}
