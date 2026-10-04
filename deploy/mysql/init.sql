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
    INDEX idx_messages_group_history (to_id, chat_type, id)
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

-- Agent 独立回帖意图；accepted 仅表示 IM 已受理，不表示群成员送达。
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
