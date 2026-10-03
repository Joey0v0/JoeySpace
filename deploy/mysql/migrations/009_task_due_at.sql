-- Run once on an existing go_im database before creating tasks with a due time.
USE go_im;

ALTER TABLE tasks
    ADD COLUMN due_at_unix_ms BIGINT NULL AFTER source_message_id;
