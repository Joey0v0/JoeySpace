-- 已有库核对并完成此前迁移后执行一次；不要重复执行。
-- 本脚本不插入机器人账号、凭证或群成员，不发送消息。
USE go_im;

CREATE TABLE im_bots (
    id           BIGINT PRIMARY KEY,
    code         VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    display_name VARCHAR(64) NOT NULL,
    status       TINYINT NOT NULL DEFAULT 1, -- 1:启用 2:停用
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY uk_im_bots_code (code)
) ENGINE=InnoDB;

ALTER TABLE messages
    ADD COLUMN sender_type TINYINT NOT NULL DEFAULT 1 AFTER from_id,
    ADD COLUMN initiator_id BIGINT NOT NULL DEFAULT 0 AFTER sender_type;

-- 旧消息由默认值保留用户发送者语义，无需将旧 from_id 改为机器人 ID。
