package agent

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func triggerResultSchemaSQL(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var statements []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "--") {
			continue
		}
		statements = append(statements, line)
	}
	return strings.Join(strings.Fields(strings.Join(statements, " ")), " ")
}

// These are static schema checks; they do not apply a migration or prove
// MySQL uniqueness, rollback or row-lock behavior on a live database.
func TestTriggerResultSchemaMigrationAddsOnlyNullableUniqueAssociation(t *testing.T) {
	sql := triggerResultSchemaSQL(t, "../../deploy/mysql/migrations/025_agent_trigger_result.sql")
	pattern := `(?i)^USE go_im; ALTER TABLE agent_task_trigger_inbox ADD COLUMN result_run_id BIGINT NULL(?: DEFAULT NULL)?, ADD UNIQUE KEY uk_agent_trigger_inbox_result \(result_run_id\);$`
	if !regexp.MustCompile(pattern).MatchString(sql) {
		t.Fatalf("025 must only add a nullable BIGINT result and its unique index, without rewriting existing receipts/budget: %s", sql)
	}
	// Legacy upgrades must leave result absent, not manufacture zero/positive
	// run IDs or infer completed from an old queued/running/exhausted record.
	for _, path := range []string{"../../deploy/mysql/migrations/023_agent_trigger_inbox.sql", "../../deploy/mysql/migrations/024_agent_trigger_lease.sql"} {
		if strings.Contains(strings.ToLower(triggerResultSchemaSQL(t, path)), "result_run_id") {
			t.Fatalf("historical migration changed result layout: %s", path)
		}
	}
}

func TestTriggerResultSchemaFreshInitializationKeepsReceiptBudgetAndUniqueResultLayout(t *testing.T) {
	sql := triggerResultSchemaSQL(t, "../../deploy/mysql/init.sql")
	table := regexp.MustCompile(`(?i)CREATE TABLE agent_task_trigger_inbox \((.*?)\) ENGINE=InnoDB;`).FindStringSubmatch(sql)
	if len(table) != 2 {
		t.Fatal("missing bounded trigger inbox initialization table")
	}
	body := strings.TrimSpace(table[1])
	for _, required := range []string{
		`message_id BIGINT NOT NULL`, `action VARCHAR\(32\) CHARACTER SET ascii COLLATE ascii_bin NOT NULL`, `event_version INT NOT NULL`,
		`status VARCHAR\(32\) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'queued'`, `received_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP`,
		`lease_token CHAR\(64\) CHARACTER SET ascii COLLATE ascii_bin NULL`, `lease_until DATETIME\(6\) NULL`,
		`model_attempts TINYINT UNSIGNED NOT NULL DEFAULT 0`, `model_started TINYINT UNSIGNED NOT NULL DEFAULT 0`,
		`result_run_id BIGINT NULL(?: DEFAULT NULL)?`, `PRIMARY KEY \(message_id\)`, `KEY idx_agent_trigger_inbox_pending \(status, message_id\)`,
		`KEY idx_agent_trigger_inbox_recovery \(status, lease_until, message_id\)`, `UNIQUE KEY uk_agent_trigger_inbox_result \(result_run_id\)`,
	} {
		if !regexp.MustCompile(`(?i)(?:^|, )` + required + `(?:,|$)`).MatchString(body) {
			t.Fatalf("fresh inbox missing or weakening %s: %s", required, body)
		}
	}
	if strings.Contains(strings.ToUpper(body), "FOREIGN KEY") || strings.Count(strings.ToLower(body), "result_run_id") != 2 {
		t.Fatal("result link must have one nullable column and one unique index, without introducing a foreign-key policy")
	}
}
