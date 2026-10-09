-- Run once after 030 and before enabling F11 mention sends. Old messages have no mention relations.
USE go_im;

CREATE TABLE im_group_message_mentions (
    message_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    mentioned_user_id BIGINT NOT NULL,
    PRIMARY KEY (message_id, mentioned_user_id),
    KEY idx_mentioned_group_message (mentioned_user_id, group_id, message_id)
) ENGINE=InnoDB;
