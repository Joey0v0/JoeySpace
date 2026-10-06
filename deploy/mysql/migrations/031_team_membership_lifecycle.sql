-- User-owned lifecycle foundation. Apply after 001_teams.sql, once, before upgrading User.
-- Existing members stay active in generation 1. No exit is performed by this migration.
USE go_im;

ALTER TABLE team_members
    ADD COLUMN membership_state TINYINT NOT NULL DEFAULT 0 COMMENT '0 active, 1 leaving, 2 left',
    ADD COLUMN generation BIGINT NOT NULL DEFAULT 1,
    ADD CONSTRAINT chk_team_members_state CHECK (membership_state IN (0, 1, 2)),
    ADD CONSTRAINT chk_team_members_generation CHECK (generation > 0);
