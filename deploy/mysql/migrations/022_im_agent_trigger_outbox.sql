-- Apply once before enabling IM Agent triggers. No historical message scan.
USE go_im;
CREATE TABLE im_agent_trigger_outbox (
    message_id BIGINT NOT NULL,
    action VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    event_version INT NOT NULL,
    msg_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    actor_id BIGINT NOT NULL,
    team_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    instruction TEXT NOT NULL,
    reference_time_ms BIGINT NOT NULL,
    published TINYINT UNSIGNED NOT NULL DEFAULT 0,
    PRIMARY KEY (message_id),
    KEY idx_im_agent_trigger_pending (published, message_id)
) ENGINE=InnoDB;
