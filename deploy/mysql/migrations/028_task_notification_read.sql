-- Run once after 027_task_status_notifications.sql on an existing go_im database.
USE go_im;

-- Historical notices start unread; confirmation retains its first database time.
ALTER TABLE task_status_notifications
    ADD COLUMN read_at TIMESTAMP NULL DEFAULT NULL AFTER created_at;
