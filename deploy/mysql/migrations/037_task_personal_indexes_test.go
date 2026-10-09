package migrations

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPersonalTaskIndexInitializationMatchesUpgrade(t *testing.T) {
	read := func(path string) string {
		t.Helper()
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		withoutComments := regexp.MustCompile(`(?m)--[^\r\n]*`).ReplaceAllString(string(content), "")
		return strings.Join(strings.Fields(withoutComments), " ")
	}

	const index = "INDEX idx_tasks_assignee_team_status_due (assignee_id, team_id, status, due_at_unix_ms, id)"
	initial := read("../init.sql")
	upgrade := read("037_task_personal_indexes.sql")

	if strings.Count(initial, index) != 1 {
		t.Fatalf("initial schema must contain the measured personal-task index once: %s", index)
	}
	wantUpgrade := "USE go_im; ALTER TABLE tasks ADD INDEX idx_tasks_assignee_team_status_due (assignee_id, team_id, status, due_at_unix_ms, id);"
	if upgrade != wantUpgrade {
		t.Fatalf("037 must only add the measured personal-task index; got: %s", upgrade)
	}
}
