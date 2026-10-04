-- Apply once after 025, before enabling the background trigger worker.
USE go_im;
ALTER TABLE agent_task_trigger_inbox
    ADD COLUMN retry_after DATETIME(6) NULL,
    ADD COLUMN retry_failures TINYINT UNSIGNED NOT NULL DEFAULT 0,
    ADD KEY idx_agent_trigger_inbox_retry (status, retry_after, message_id);
