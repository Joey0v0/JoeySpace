-- Run once after 007_task_operations.sql on an existing go_im database.
USE go_im;

-- Task owns personal notices for actual status transitions.
-- A unique operation/recipient pair prevents duplicate recipients.
CREATE TABLE task_status_notifications (
    id           BIGINT PRIMARY KEY AUTO_INCREMENT,
    operation_id BIGINT NOT NULL,
    team_id      BIGINT NOT NULL,
    task_id      BIGINT NOT NULL,
    recipient_id BIGINT NOT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_task_status_notice_operation_recipient (operation_id, recipient_id),
    KEY idx_task_status_notice_recipient (recipient_id, id)
) ENGINE=InnoDB;
