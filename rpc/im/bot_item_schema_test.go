package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestBotItemSendSchemaPreservesLegacyAndMatchesMigration(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		withoutComments := regexp.MustCompile(`(?m)--[^\r\n]*`).ReplaceAllString(string(data), "")
		return strings.Join(strings.Fields(withoutComments), " ")
	}
	old := read("../../deploy/mysql/migrations/014_im_bot_sends.sql")
	init := read("../../deploy/mysql/init.sql")
	migration := read("../../deploy/mysql/migrations/020_im_bot_send_items.sql")
	if migration != "USE go_im; ALTER TABLE im_bot_sends ADD COLUMN item_index INT NOT NULL DEFAULT 0 AFTER run_id;" {
		t.Fatalf("migration must only add a default-zero column: %s", migration)
	}
	tablePattern := regexp.MustCompile(`CREATE TABLE im_bot_sends \(.*?\) ENGINE=InnoDB;`)
	oldTable, newTable := tablePattern.FindString(old), tablePattern.FindString(init)
	if oldTable == "" || newTable == "" {
		t.Fatal("missing send schema")
	}
	expected := strings.Replace(oldTable, "run_id BIGINT NOT NULL,", "run_id BIGINT NOT NULL, item_index INT NOT NULL DEFAULT 0,", 1)
	if expected == oldTable || expected != newTable {
		t.Fatalf("new initialization does not match upgrading 014 with 020:\n%s\n%s", expected, newTable)
	}
}
