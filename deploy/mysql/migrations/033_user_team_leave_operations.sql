-- User-owned durable leave intent. Apply once after 001 and 031, before enabling leave.
-- This migration records no operation and does not revoke or clean up memberships.
USE go_im;

CREATE TABLE user_team_leave_operations (
    id BIGINT NOT NULL AUTO_INCREMENT,
    team_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    request_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    generation BIGINT NOT NULL,
    status TINYINT NOT NULL DEFAULT 0 COMMENT '0 awaiting IM cleanup, 1 completed',
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    completed_at DATETIME(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_user_team_leave_request (user_id, request_key),
    UNIQUE KEY uk_user_team_leave_generation (team_id, user_id, generation),
    CONSTRAINT chk_user_team_leave_scope CHECK (team_id > 0 AND user_id > 0 AND generation > 0),
    CONSTRAINT chk_user_team_leave_status CHECK (status IN (0, 1)),
    CONSTRAINT chk_user_team_leave_completion CHECK ((status = 0 AND completed_at IS NULL) OR (status = 1 AND completed_at IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
