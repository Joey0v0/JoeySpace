-- IM owns durable send intentions and Kafka acceptance, not Agent execution.
-- Run once after 013; this is not a background outbox dispatcher.
CREATE TABLE im_bot_sends (
    msg_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
    run_id BIGINT NOT NULL,
    bot_id BIGINT NOT NULL,
    initiator_id BIGINT NOT NULL,
    team_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    content TEXT NOT NULL,
    timestamp_unix_ms BIGINT NOT NULL,
    accepted TINYINT UNSIGNED NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB;
