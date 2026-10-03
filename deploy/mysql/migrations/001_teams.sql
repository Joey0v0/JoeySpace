-- 阶段 2：团队与成员。用于已有 go_im 数据库；不修改旧群聊表。
USE go_im;

CREATE TABLE IF NOT EXISTS teams (
    id         BIGINT      NOT NULL PRIMARY KEY,   -- Snowflake ID
    name       VARCHAR(64) NOT NULL,
    owner_id   BIGINT      NOT NULL,
    created_at TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_teams_owner (owner_id),
    CONSTRAINT fk_teams_owner FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS team_members (
    team_id   BIGINT    NOT NULL,
    user_id   BIGINT    NOT NULL,
    role      TINYINT   NOT NULL DEFAULT 0,        -- 0:成员 1:管理员 2:团队拥有者
    joined_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (team_id, user_id),
    INDEX idx_team_members_user (user_id, team_id),
    CONSTRAINT fk_team_members_team FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE,
    CONSTRAINT fk_team_members_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_team_members_role CHECK (role IN (0, 1, 2))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
