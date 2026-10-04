-- Apply once after 023. This prepares execution state; it does not enable a worker.
USE go_im;
ALTER TABLE agent_task_trigger_inbox
    ADD COLUMN lease_token CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    ADD COLUMN lease_until DATETIME(6) NULL,
    ADD COLUMN model_attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
    ADD COLUMN model_started TINYINT UNSIGNED NOT NULL DEFAULT 0,
    ADD KEY idx_agent_trigger_inbox_recovery (status, lease_until, message_id);
