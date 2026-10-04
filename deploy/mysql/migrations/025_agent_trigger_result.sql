-- Apply once after 024, before enabling background draft generation.
USE go_im;
ALTER TABLE agent_task_trigger_inbox
    ADD COLUMN result_run_id BIGINT NULL,
    ADD UNIQUE KEY uk_agent_trigger_inbox_result (result_run_id);
