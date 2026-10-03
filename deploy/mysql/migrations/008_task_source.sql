-- Run once on an existing go_im database before creating tasks with a source message.
USE go_im;

ALTER TABLE tasks
    ADD COLUMN source_group_id BIGINT NULL AFTER assignee_id,
    ADD COLUMN source_message_id BIGINT NULL AFTER source_group_id;
