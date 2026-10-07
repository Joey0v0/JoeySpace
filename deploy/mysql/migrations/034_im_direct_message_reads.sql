-- Run once on an existing go_im database. Existing direct messages stay unread until explicitly marked.
USE go_im;

CREATE TABLE im_direct_message_reads (
    user_id BIGINT NOT NULL,
    peer_id BIGINT NOT NULL,
    message_id BIGINT NOT NULL,
    read_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id, peer_id, message_id)
) ENGINE=InnoDB;
