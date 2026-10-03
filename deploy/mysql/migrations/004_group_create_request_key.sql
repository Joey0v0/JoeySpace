-- Run once after 003_group_team_id.sql on an existing go_im database.
-- Legacy groups keep NULL; MySQL permits multiple NULL values in a unique key.
USE go_im;

ALTER TABLE `groups`
    ADD COLUMN request_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER team_id,
    ADD UNIQUE KEY uk_groups_owner_request (owner_id, request_key);
