-- Run once after 006_tasks.sql on an existing go_im database.
USE go_im;

CREATE TABLE task_operations (
    id          BIGINT PRIMARY KEY AUTO_INCREMENT,
    task_id     BIGINT NOT NULL,
    actor_id    BIGINT NOT NULL,
    from_status TINYINT NOT NULL,
    to_status   TINYINT NOT NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_task_operations_task (task_id, id)
) ENGINE=InnoDB;
