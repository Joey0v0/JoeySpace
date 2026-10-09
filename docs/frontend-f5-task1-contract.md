# F5 任务 1 共同契约与执行边界

日期：2026-10-10。主工作区：`D:/zy/GoLang/JoeySpace/.worktrees/frontend-f5-ai-draft`，分支 `codex/frontend-f5-ai-draft`。用户确认设计与计划后的共同基线是 `a851fb8`；执行工作区从包含本契约的后续共同提交建立，由主 agent 告知确切提交。

## 目标、原因和问题

本步只完成本人已保存 `@AI 整理任务` 消息资格，以及 Trigger、1—5 项草稿集合和单项响应的 TypeScript 严格解码。页面和 API 接线依赖这些边界；本步用测试证明大整数 ID、会话范围、状态和解析依据不被误读。任务要求以[八任务计划的任务 1](superpowers/plans/2026-10-10-frontend-f5-ai-draft.md#任务-1锁定原消息资格与-agent-响应解码)及[F5 设计](frontend-f5-ai-draft-design.md)为准。

## 执行工作区和允许文件

执行 agent 工作区固定为 `D:/zy/GoLang/JoeySpace/.worktrees/frontend-f5-model`，分支 `codex/frontend-f5-model`。仅允许新增或编辑：

- `frontend/src/agent/model.ts`
- `frontend/src/agent/model.test.ts`

执行 agent 不修改 Gateway、RPC、迁移、依赖、`package.json`、页面、共同文档或其他测试；不自行提交、合并 main、推送、部署或派生子 agent。主 agent 独占共同文档、Git 保存、测试复核与集成。执行者在自己的 worktree 按 TDD 实施、运行定向测试和类型检查，并提供测试红绿证据与文件 diff 供主 agent 审查。

## 共享数据规则

- Trigger 入口仅来自本人当前团队群的正持久消息 ID、普通用户 `sender_type` 0/1、`initiator_id="0"`、文本 `content_type=1` 与完整 `@AI 整理任务 <要求>` 命令；任何缺失的消息元数据都不能以猜测替代。浏览器判定只决定按钮可见性，服务端状态接口仍负责授权。
- 状态响应的 `message_id/team_id/group_id` 必须等于请求范围。状态只允许 `queued/running/exhausted/completed`；只有 `completed` 可给正 `run_id`，其他状态的 `run_id` 为 `"0"`。
- 集合的 `run_id/team_id/group_id` 必须等于从 Trigger 得到的范围；`item_count` 为 1—5，项索引连续 0 起。单项也必须核对运行与范围。草稿 `source_message_id` 是讨论依据，可与触发命令消息不同；仅校验其十进制字符串与允许零值，不要求与 Trigger 消息 ID 相等。
- 所有 64 位 ID 和 `revision` 保留十进制字符串；`due_at_unix_ms` 与期限依据时间为 JSON 安全整数。无效、不完整或自相矛盾的服务端回包返回 `null`，后续页面显示错误而不是部分展示私有数据。

## 审查与下一步

主 agent 验证定向测试、类型检查、diff 与上述边界后，安排独立只读规格/质量审查；通过后集成到主 F5 分支。本契约不授权提前做任务 2 的 HTTP/超时接线。
