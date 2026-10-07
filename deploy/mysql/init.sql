CREATE DATABASE IF NOT EXISTS go_im DEFAULT CHARSET utf8mb4 COLLATE utf8mb4_unicode_ci;
USE go_im;

-- 用户表
CREATE TABLE users (
    id         BIGINT PRIMARY KEY,                 -- Snowflake ID
    username   VARCHAR(32)  NOT NULL UNIQUE,
    password   VARCHAR(128) NOT NULL,              -- bcrypt hash
    nickname   VARCHAR(64)  NOT NULL DEFAULT '',
    avatar     VARCHAR(256) NOT NULL DEFAULT '',
    status     TINYINT      NOT NULL DEFAULT 1,    -- 1:正常 2:封禁
    created_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_username (username)
) ENGINE=InnoDB;

-- User团队基础；已有卷先按001/031迁移，不通过重新执行init升级。
CREATE TABLE teams (
    id BIGINT NOT NULL PRIMARY KEY,
    name VARCHAR(64) NOT NULL,
    owner_id BIGINT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_teams_owner (owner_id),
    CONSTRAINT fk_teams_owner FOREIGN KEY (owner_id) REFERENCES users(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE team_members (
    team_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    role TINYINT NOT NULL DEFAULT 0,
    joined_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    membership_state TINYINT NOT NULL DEFAULT 0 COMMENT '0 active, 1 leaving, 2 left',
    generation BIGINT NOT NULL DEFAULT 1,
    PRIMARY KEY (team_id, user_id),
    INDEX idx_team_members_user (user_id, team_id),
    CONSTRAINT fk_team_members_team FOREIGN KEY (team_id) REFERENCES teams(id) ON DELETE CASCADE,
    CONSTRAINT fk_team_members_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT,
    CONSTRAINT chk_team_members_role CHECK (role IN (0, 1, 2)),
    CONSTRAINT chk_team_members_state CHECK (membership_state IN (0, 1, 2)),
    CONSTRAINT chk_team_members_generation CHECK (generation > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- User退出操作：先持久保存撤权意图，再在后续业务步骤调用IM清理。
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

-- 好友关系表
CREATE TABLE friendships (
    id         BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id    BIGINT   NOT NULL,
    friend_id  BIGINT   NOT NULL,
    status     TINYINT  NOT NULL DEFAULT 0,        -- 0:待确认 1:已接受
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_user_friend (user_id, friend_id),
    INDEX idx_friend_id (friend_id)
) ENGINE=InnoDB;

-- 群组表
CREATE TABLE `groups` (
    id          BIGINT PRIMARY KEY,                -- Snowflake ID
    name        VARCHAR(64) NOT NULL,
    owner_id    BIGINT      NOT NULL,
    team_id     BIGINT      NULL,                   -- NULL:旧群；非空:所属团队，由 IM 服务验证
    request_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL, -- 团队群创建请求键；旧群为 NULL
    avatar      VARCHAR(256) NOT NULL DEFAULT '',
    created_at  TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_groups_team (team_id, id),
    UNIQUE KEY uk_groups_owner_request (owner_id, request_key)
) ENGINE=InnoDB;

-- 群成员表
CREATE TABLE group_members (
    id         BIGINT PRIMARY KEY AUTO_INCREMENT,
    group_id   BIGINT  NOT NULL,
    user_id    BIGINT  NOT NULL,
    role       TINYINT NOT NULL DEFAULT 0,         -- 0:普通 1:管理员 2:群主
    joined_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uk_group_user (group_id, user_id)
) ENGINE=InnoDB;

-- IM 拥有机器人资料；不建立用户登录账号或普通群成员资格。
CREATE TABLE im_bots (
    id           BIGINT PRIMARY KEY,
    code         VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    display_name VARCHAR(64) NOT NULL,
    status       TINYINT NOT NULL DEFAULT 1, -- 1:启用 2:停用
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_im_bots_code (code)
) ENGINE=InnoDB;

-- IM 的机器人发送记录；先固定内容，再同步写 Kafka，无后台派发。
CREATE TABLE im_bot_sends (
    msg_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
    run_id BIGINT NOT NULL,
    item_index INT NOT NULL DEFAULT 0, -- 固定草稿项序号；旧单项记录为 0
    bot_id BIGINT NOT NULL,
    initiator_id BIGINT NOT NULL,
    team_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    content TEXT NOT NULL,
    timestamp_unix_ms BIGINT NOT NULL,
    accepted TINYINT UNSIGNED NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB;

-- 消息主表
CREATE TABLE messages (
    id           BIGINT PRIMARY KEY,                -- Snowflake ID，自然有序
    msg_id       VARCHAR(64)  NOT NULL UNIQUE,      -- 客户端生成的 UUID，防重幂等
    from_id      BIGINT       NOT NULL,
    sender_type  TINYINT      NOT NULL DEFAULT 1,   -- 1:用户 2:IM 机器人；from_id 按类型解释
    initiator_id BIGINT       NOT NULL DEFAULT 0,   -- 机器人操作的授权用户；普通消息为 0
    to_id        BIGINT       NOT NULL,             -- 接收人ID 或 群组ID
    chat_type    TINYINT      NOT NULL,             -- 1:单聊 2:群聊
    content_type TINYINT      NOT NULL DEFAULT 1,   -- 1:文本 2:图片 3:文件
    content      TEXT         NOT NULL,
    created_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_to_time (to_id, created_at),
    INDEX idx_messages_group_history (to_id, chat_type, id),
    INDEX idx_messages_direct_pair (from_id, to_id, chat_type, id)
) ENGINE=InnoDB;

-- 离线消息表
CREATE TABLE offline_messages (
    id         BIGINT PRIMARY KEY AUTO_INCREMENT,
    user_id    BIGINT  NOT NULL,                    -- 接收方ID
    message_id BIGINT  NOT NULL,                    -- 关联 messages 表的 ID
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_user_id (user_id),
    UNIQUE KEY uk_offline_user_message (user_id, message_id)
) ENGINE=InnoDB;

-- 任务服务拥有的任务表；来源只存 IM 资源 ID，不跨服务建外键。
CREATE TABLE tasks (
    id          BIGINT PRIMARY KEY,
    team_id     BIGINT NOT NULL,
    title       VARCHAR(200) NOT NULL,
    description TEXT NOT NULL,
    creator_id  BIGINT NOT NULL,
    assignee_id BIGINT NULL,
    source_group_id BIGINT NULL,
    source_message_id BIGINT NULL,
    due_at_unix_ms BIGINT NULL, -- UTC Unix 毫秒；NULL 表示未设置
    status      TINYINT NOT NULL DEFAULT 0, -- 0:待办 1:进行中 2:完成
    request_key VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    request_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, -- 创建时规范化请求摘要，不随任务编辑变化
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_tasks_team (team_id, id),
    UNIQUE KEY uk_tasks_creator_request (creator_id, request_key)
) ENGINE=InnoDB;

-- 任务服务的状态操作记录；与任务状态更新在同一事务中写入。
CREATE TABLE task_operations (
    id          BIGINT PRIMARY KEY AUTO_INCREMENT,
    task_id     BIGINT NOT NULL,
    actor_id    BIGINT NOT NULL,
    from_status TINYINT NOT NULL,
    to_status   TINYINT NOT NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    INDEX idx_task_operations_task (task_id, id)
) ENGINE=InnoDB;

-- Task 服务的个人状态通知；只对应真实状态变更的操作记录。
CREATE TABLE task_status_notifications (
    id           BIGINT PRIMARY KEY AUTO_INCREMENT,
    operation_id BIGINT NOT NULL,
    team_id      BIGINT NOT NULL,
    task_id      BIGINT NOT NULL,
    recipient_id BIGINT NOT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    read_at      TIMESTAMP NULL DEFAULT NULL,
    UNIQUE KEY uk_task_status_notice_operation_recipient (operation_id, recipient_id),
    KEY idx_task_status_notice_recipient (recipient_id, id)
) ENGINE=InnoDB;

-- Agent 服务拥有运行及任务草稿；先存一项，item_index 为后续多项保留。
CREATE TABLE agent_runs (
    id           BIGINT PRIMARY KEY,
    team_id      BIGINT NOT NULL,
    group_id     BIGINT NOT NULL,
    initiator_id BIGINT NOT NULL,
    request_key  VARCHAR(64) NULL,
    request_fingerprint CHAR(64) NULL,
    status       VARCHAR(32) NOT NULL,
    draft_mode   VARCHAR(16) NOT NULL DEFAULT 'single',
    item_count   SMALLINT UNSIGNED NOT NULL DEFAULT 1,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_agent_runs_initiator_request (initiator_id, request_key)
) ENGINE=InnoDB;

CREATE TABLE agent_task_drafts (
    run_id            BIGINT NOT NULL,
    item_index        SMALLINT UNSIGNED NOT NULL,
    status            VARCHAR(32) NOT NULL DEFAULT '', -- legacy single reads run status; collection has item status
    title             VARCHAR(200) NOT NULL,
    description       TEXT NOT NULL,
    assignee_id       BIGINT NOT NULL DEFAULT 0,
    assignee_name     VARCHAR(64) NOT NULL DEFAULT '',
    assignee_resolution VARCHAR(16) NOT NULL DEFAULT '', -- legacy empty; none/matched/not_found/ambiguous/truncated
    revision          BIGINT NOT NULL DEFAULT 1,
    due_at_unix_ms    BIGINT NOT NULL DEFAULT 0,
    deadline_text VARCHAR(200) NOT NULL DEFAULT '',
    deadline_source VARCHAR(16) NOT NULL DEFAULT '',
    deadline_source_message_id BIGINT NOT NULL DEFAULT 0,
    deadline_reference_unix_ms BIGINT NOT NULL DEFAULT 0,
    deadline_timezone VARCHAR(64) NOT NULL DEFAULT '',
    deadline_resolution VARCHAR(16) NOT NULL DEFAULT '',
    deadline_reason VARCHAR(40) NOT NULL DEFAULT '',
    deadline_parsed_unix_ms BIGINT NOT NULL DEFAULT 0,
    instruction_reference_unix_ms BIGINT NOT NULL DEFAULT 0,
    source_message_id BIGINT NOT NULL DEFAULT 0,
    task_request_key  VARCHAR(64) NULL,
    task_id           BIGINT NULL,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (run_id, item_index)
) ENGINE=InnoDB;

-- IM trigger intent; source timestamp is persisted epoch milliseconds, not worker time.
CREATE TABLE im_agent_trigger_outbox (
    message_id BIGINT NOT NULL,
    action VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    event_version INT NOT NULL,
    msg_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    actor_id BIGINT NOT NULL,
    team_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    instruction TEXT NOT NULL,
    reference_time_ms BIGINT NOT NULL,
    published TINYINT UNSIGNED NOT NULL DEFAULT 0,
    PRIMARY KEY (message_id),
    KEY idx_im_agent_trigger_pending (published, message_id)
) ENGINE=InnoDB;

-- Agent durable notification intake; queued is not a generated draft.
CREATE TABLE agent_task_trigger_inbox (
    message_id BIGINT NOT NULL,
    action VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    event_version INT NOT NULL,
    status VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'queued',
    received_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    lease_token CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    lease_until DATETIME(6) NULL,
    model_attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
    model_started TINYINT UNSIGNED NOT NULL DEFAULT 0,
    result_run_id BIGINT NULL,
    retry_after DATETIME(6) NULL,
    retry_failures TINYINT UNSIGNED NOT NULL DEFAULT 0,
    PRIMARY KEY (message_id),
    KEY idx_agent_trigger_inbox_pending (status, message_id),
    KEY idx_agent_trigger_inbox_recovery (status, lease_until, message_id),
    UNIQUE KEY uk_agent_trigger_inbox_result (result_run_id),
    KEY idx_agent_trigger_inbox_retry (status, retry_after, message_id)
) ENGINE=InnoDB;

-- Agent 独立回帖意图；accepted 仅表示 IM 已受理，不表示群成员送达。
CREATE TABLE agent_task_replies (
    run_id       BIGINT NOT NULL,
    item_index   INT NOT NULL DEFAULT 0,
    task_id      BIGINT NOT NULL,
    team_id      BIGINT NOT NULL,
    group_id     BIGINT NOT NULL,
    initiator_id BIGINT NOT NULL,
    msg_id       VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    content      TEXT NOT NULL,
    accepted     TINYINT UNSIGNED NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (run_id, item_index),
    UNIQUE KEY uk_agent_task_reply_msg (msg_id)
) ENGINE=InnoDB;

-- Task pending hints; published means Kafka ACK, not browser read.
CREATE TABLE task_notification_outbox (
    notification_id BIGINT PRIMARY KEY,
    team_id BIGINT NOT NULL,
    recipient_id BIGINT NOT NULL,
    event_version INT NOT NULL,
    published BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    published_at TIMESTAMP NULL DEFAULT NULL,
    KEY idx_task_notification_outbox_pending (published, notification_id)
) ENGINE=InnoDB;

-- Personal explicit group-message reads; no message-ID high-water mark.
CREATE TABLE im_group_message_reads (
    user_id BIGINT NOT NULL,
    group_id BIGINT NOT NULL,
    message_id BIGINT NOT NULL,
    read_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id, group_id, message_id)
) ENGINE=InnoDB;

-- Personal explicit direct-message reads; only incoming messages may be marked by the recipient.
CREATE TABLE im_direct_message_reads (
    user_id BIGINT NOT NULL,
    peer_id BIGINT NOT NULL,
    message_id BIGINT NOT NULL,
    read_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (user_id, peer_id, message_id)
) ENGINE=InnoDB;

-- Permanent IM closure boundary, shared by future team joins and exit cleanup.
CREATE TABLE IF NOT EXISTS im_team_group_fences (
    team_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    closed_through_generation BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (team_id, user_id),
    CONSTRAINT chk_im_team_group_fence_ids CHECK (team_id > 0 AND user_id > 0),
    CONSTRAINT chk_im_team_group_fence_generation CHECK (closed_through_generation >= 0)
) ENGINE=InnoDB;
