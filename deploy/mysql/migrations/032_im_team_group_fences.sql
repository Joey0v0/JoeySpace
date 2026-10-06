-- IM-owned permanent boundary. No member is closed or removed by this migration.
USE go_im;

CREATE TABLE IF NOT EXISTS im_team_group_fences (
    team_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    closed_through_generation BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (team_id, user_id),
    CONSTRAINT chk_im_team_group_fence_ids CHECK (team_id > 0 AND user_id > 0),
    CONSTRAINT chk_im_team_group_fence_generation CHECK (closed_through_generation >= 0)
) ENGINE=InnoDB;
