package repository

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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
