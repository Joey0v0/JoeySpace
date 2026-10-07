-- Run once after 034 on an existing go_im database.
-- One pair index covers both user->peer and peer->user branches.
USE go_im;

ALTER TABLE messages
    ADD INDEX idx_messages_direct_pair (from_id, to_id, chat_type, id);
