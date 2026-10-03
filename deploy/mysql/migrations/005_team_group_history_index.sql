-- Run once on an existing go_im database before enabling team group history.
-- The index supports group messages ordered by descending message ID.
USE go_im;

ALTER TABLE messages
    ADD INDEX idx_messages_group_history (to_id, chat_type, id);
