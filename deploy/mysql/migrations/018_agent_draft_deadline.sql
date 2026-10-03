-- Run once after 017 before upgrading Agent. Legacy evidence remains empty;
-- do not reinterpret existing deadlines, request keys or task results.
USE go_im;

ALTER TABLE agent_task_drafts
    ADD COLUMN deadline_text VARCHAR(200) NOT NULL DEFAULT '',
    ADD COLUMN deadline_source VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN deadline_source_message_id BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN deadline_reference_unix_ms BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN deadline_timezone VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN deadline_resolution VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN deadline_reason VARCHAR(40) NOT NULL DEFAULT '',
    ADD COLUMN deadline_parsed_unix_ms BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN instruction_reference_unix_ms BIGINT NOT NULL DEFAULT 0;
