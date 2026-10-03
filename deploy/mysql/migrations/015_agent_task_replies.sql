-- 先完成 010/011/012；Agent 拥有独立回帖状态，不改变任务成功状态。
USE go_im;

CREATE TABLE agent_task_replies (
    run_id       BIGINT PRIMARY KEY,
    task_id      BIGINT NOT NULL,
    team_id      BIGINT NOT NULL,
    group_id     BIGINT NOT NULL,
    initiator_id BIGINT NOT NULL,
    msg_id       VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    content      TEXT NOT NULL,
    accepted     TINYINT UNSIGNED NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_agent_task_reply_msg (msg_id)
) ENGINE=InnoDB;
