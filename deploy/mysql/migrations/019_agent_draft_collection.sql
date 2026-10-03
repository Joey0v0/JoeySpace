-- Run once after 018 and before upgrading Agent; no existing keys/results change.
USE go_im;

ALTER TABLE agent_runs
    ADD COLUMN draft_mode VARCHAR(16) NOT NULL DEFAULT 'single',
    ADD COLUMN item_count SMALLINT UNSIGNED NOT NULL DEFAULT 1;

-- Existing single runs still read their authoritative state from agent_runs.
-- Collection items always store an explicit state; never treat empty as waiting.
ALTER TABLE agent_task_drafts
    ADD COLUMN status VARCHAR(32) NOT NULL DEFAULT '';
