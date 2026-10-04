-- Run once after 014 in go_im. IM owns these records; no Agent table is accessed.
-- Existing IDs, contents, timestamps and acceptance are preserved as item 0.
USE go_im;
ALTER TABLE im_bot_sends
    ADD COLUMN item_index INT NOT NULL DEFAULT 0 AFTER run_id;
