-- Run once after migrations 010/011/012, before enabling Agent name resolution.
-- Existing drafts keep empty metadata and their original assignee/task fields.
USE go_im;

ALTER TABLE agent_task_drafts
    ADD COLUMN assignee_name VARCHAR(64) NOT NULL DEFAULT '' AFTER assignee_id,
    ADD COLUMN assignee_resolution VARCHAR(16) NOT NULL DEFAULT '' AFTER assignee_name;
