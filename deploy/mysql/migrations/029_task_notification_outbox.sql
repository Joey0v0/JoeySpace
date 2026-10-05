-- Run once after 028_task_notification_read.sql on an existing go_im database.
USE go_im;

-- Only new status notices enqueue hints. Historical notices remain queryable.
CREATE TABLE task_notification_outbox (
    notification_id BIGINT PRIMARY KEY,
    team_id BIGINT NOT NULL,
    recipient_id BIGINT NOT NULL,
    event_version INT NOT NULL,
    published BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMP NULL DEFAULT NULL,
    KEY idx_task_notification_outbox_pending (published, notification_id)
) ENGINE=InnoDB;
