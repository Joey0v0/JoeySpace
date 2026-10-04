-- Run once after 015/019; Agent owns replies, not IM message acceptance.
-- Existing single-item cards, IDs and acceptance remain at item_index 0.
USE go_im;
ALTER TABLE agent_task_replies
    ADD COLUMN item_index INT NOT NULL DEFAULT 0 AFTER run_id,
    DROP PRIMARY KEY,
    ADD PRIMARY KEY (run_id, item_index);
