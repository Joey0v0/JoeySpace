-- Existing go_im databases only. Review the query result before running ALTER TABLE.
-- The migration intentionally does not delete any existing offline records.
USE go_im;

SELECT user_id, message_id, COUNT(*) AS duplicate_count
FROM offline_messages
GROUP BY user_id, message_id
HAVING COUNT(*) > 1;

-- If the query above returns rows, resolve those duplicates before this statement.
ALTER TABLE offline_messages
    ADD UNIQUE KEY uk_offline_user_message (user_id, message_id);
