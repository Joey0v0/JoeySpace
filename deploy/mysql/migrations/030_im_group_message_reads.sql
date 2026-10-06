-- Run once on an existing go_im database. No history is automatically marked read.
USE go_im;

CREATE TABLE im_group_message_reads (
    user_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    message_id BIGINT NOT NULL,
    read_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id, group_id, message_id)
) ENGINE=InnoDB;
