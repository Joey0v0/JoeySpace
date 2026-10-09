USE go_im;

ALTER TABLE tasks
    ADD INDEX idx_tasks_assignee_team_status_due (assignee_id, team_id, status, due_at_unix_ms, id);
