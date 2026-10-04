-- Apply once before enabling Agent notification intake. Does not generate drafts.
USE go_im;
CREATE TABLE agent_task_trigger_inbox (
    message_id BIGINT NOT NULL,
    action VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    event_version INT NOT NULL,
    status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'queued',
    received_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (message_id),
    KEY idx_agent_trigger_inbox_pending (status, message_id)
) ENGINE=InnoDB;
