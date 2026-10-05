package main

import (
	"math"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/yjydist/go-im/internal/model"
)

func readTaskNotificationSchema(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func normalizeTaskNotificationSchema(sql string) string {
	withoutComments := regexp.MustCompile(`(?m)--[^\r\n]*`).ReplaceAllString(sql, "")
	return strings.Join(strings.Fields(withoutComments), " ")
}

func TestTaskNotificationOutboxInitializationMatchesUpgrade(t *testing.T) {
	init := normalizeTaskNotificationSchema(readTaskNotificationSchema(t, "../../deploy/mysql/init.sql"))
	upgrade := normalizeTaskNotificationSchema(readTaskNotificationSchema(t, "../../deploy/mysql/migrations/029_task_notification_outbox.sql"))
	definition := regexp.MustCompile(`CREATE TABLE task_notification_outbox \(.*?\) ENGINE=InnoDB;`)
	initialTable, upgradedTable := definition.FindString(init), definition.FindString(upgrade)
	if initialTable == "" || initialTable != upgradedTable {
		t.Fatalf("initialization and 029 table definitions differ:\ninit: %s\n029: %s", initialTable, upgradedTable)
	}
	// The upgrade creates an empty queue; no existing notice is unexpectedly delivered.
	if upgrade != "USE go_im; "+upgradedTable {
		t.Fatalf("029 must only create the empty outbox, without backfilling or mutating notices: %s", upgrade)
	}
	for _, requirement := range []string{
		"notification_id BIGINT PRIMARY KEY", "team_id BIGINT NOT NULL", "recipient_id BIGINT NOT NULL",
		"event_version INT NOT NULL", "published BOOLEAN NOT NULL DEFAULT FALSE",
		"created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP", "published_at TIMESTAMP NULL DEFAULT NULL",
		"KEY idx_task_notification_outbox_pending (published, notification_id)",
	} {
		if !strings.Contains(upgradedTable, requirement) {
			t.Fatalf("outbox lost required identity, pending lookup or initial state: %s", requirement)
		}
	}
	if strings.Contains(upgradedTable, "AUTO_INCREMENT") || strings.Contains(upgradedTable, "ON UPDATE") {
		t.Fatal("outbox must reuse notice ID and retain first publication time")
	}
	columns := regexp.MustCompile(`(?:\(|,)\s*(\w+)\s+\w+`).FindAllStringSubmatch(upgradedTable, -1)
	var names []string
	for _, column := range columns {
		names = append(names, column[1])
	}
	wantNames := []string{"notification_id", "team_id", "recipient_id", "event_version", "published", "created_at", "published_at", "KEY"}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("outbox must store only routing/publication metadata, got definitions %v", names)
	}
}

func TestTaskNotificationOutboxUpgradeDocumentsReadMigrationPrerequisite(t *testing.T) {
	upgrade := readTaskNotificationSchema(t, "../../deploy/mysql/migrations/029_task_notification_outbox.sql")
	if !strings.Contains(upgrade, "after 028_task_notification_read.sql") {
		t.Fatal("029 must document the preceding 028 read-state migration")
	}
	previous := normalizeTaskNotificationSchema(readTaskNotificationSchema(t, "../../deploy/mysql/migrations/028_task_notification_read.sql"))
	init := normalizeTaskNotificationSchema(readTaskNotificationSchema(t, "../../deploy/mysql/init.sql"))
	if !strings.Contains(previous, "ALTER TABLE task_status_notifications ADD COLUMN read_at TIMESTAMP NULL DEFAULT NULL") ||
		!strings.Contains(init, "read_at TIMESTAMP NULL DEFAULT NULL") {
		t.Fatal("outbox prerequisite must preserve unread history for existing and new databases")
	}
}

func TestTaskNotificationOutboxRowMapsStableEventReferences(t *testing.T) {
	row := taskNotificationOutboxRow{NotificationID: math.MaxInt64, TeamID: 9007199254740993,
		RecipientID: math.MaxInt64 - 1, EventVersion: 1}
	if row.TableName() != "task_notification_outbox" {
		t.Fatalf("row uses another owner's table: %s", row.TableName())
	}
	want := model.TaskNotificationEvent{Version: 1, Type: "task_notification_changed", NotificationID: math.MaxInt64,
		TeamID: 9007199254740993, RecipientID: math.MaxInt64 - 1}
	if row.Event() != want {
		t.Fatalf("row loses event identity: %+v", row.Event())
	}
	if err := row.Validate(); err != nil {
		t.Fatal(err)
	}
	row.Published = true
	if row.Event() != want {
		t.Fatal("publication acknowledgement must not change retry payload")
	}
	row.EventVersion = 2
	if err := row.Validate(); err == nil {
		t.Fatal("persisted unsupported version must not produce a publishable event")
	}
}
