-- Run once on an existing go_im database before starting task-rpc.
USE go_im;

CREATE TABLE tasks (
    id          BIGINT PRIMARY KEY,
    team_id     BIGINT NOT NULL,
    title       VARCHAR(200) NOT NULL,
    description TEXT NOT NULL,
    creator_id  BIGINT NOT NULL,
    assignee_id BIGINT NULL,
    status      TINYINT NOT NULL DEFAULT 0, -- 0:待办 1:进行中 2:完成
    request_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, -- 创建时规范化请求摘要，不随任务编辑变化
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_tasks_team (team_id, id),
    UNIQUE KEY uk_tasks_creator_request (creator_id, request_key)
) ENGINE=InnoDB;
