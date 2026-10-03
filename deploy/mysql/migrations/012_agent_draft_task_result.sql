-- 已有库按顺序执行 010、011 后再执行本迁移；不要重复执行。
-- 确认冻结草稿后保存稳定创建键；任务 ID 只在 Task 返回成功后记录。
USE go_im;

ALTER TABLE agent_task_drafts
    ADD COLUMN task_request_key VARCHAR(64) NULL AFTER source_message_id,
    ADD COLUMN task_id BIGINT NULL AFTER task_request_key;
