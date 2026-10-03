-- Run once after 016 before upgrading Agent; existing intents keep version 1.
USE go_im;

ALTER TABLE agent_task_drafts
    ADD COLUMN revision BIGINT NOT NULL DEFAULT 1 AFTER assignee_resolution;
