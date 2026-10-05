# 群内 @AI 后台草稿发现入口契约

日期：2026-10-05。用户选 A：从原指令消息查看状态和草稿，不做“我的后台运行”列表。沿 A36（仅发起人）和 A60（持久来源、当前资格），不改变服务归属、身份方案或模型调用。消息历史已经携带稳定的十进制字符串消息 ID，因此本批不新增索引、迁移或轮询队列。

## 业务与权限

1. 浏览器在当前团队群的**本人** `@AI 整理任务` 原消息上显示“查看 AI 草稿”。点击时用该已保存消息 ID 调用只读 HTTP：`GET /api/v1/teams/:team_id/groups/:group_id/agent-triggers/:message_id`，带本人 Bearer Token。页面不能自行提交 actor、run ID、执行状态或群归属；未落库/不符合指令的消息不展示入口。刷新后可从群历史再次找到原消息。
2. Gateway 只校验路径十进制正 ID、Token，转发 `GetTaskTriggerStatus(message_id)`，核对返回的 message/team/group 与请求范围完全一致，并校验状态/run ID 组合。HTTP 返回 `data: {message_id, team_id, group_id, status, run_id}`，所有 ID 是字符串；状态仅 `queued|running|exhausted|completed`。只有 completed 的 run_id 为正；其他状态为字符串 `"0"`。不得返回租约、失败次数、模型错误、群消息内容或访问令牌。
3. Agent 先由现有 User RPC 从 Token 解析当前用户，再经已有专用 mTLS IM 来源 RPC 用 message_id 读取保存的 actor/team/group/原消息与当前团队群资格；必须核对 actor 等于当前用户。不存在、不是本人或非有效来源不返回运行状态。仅在鉴权通过后按 message_id 读取 Agent inbox，严格校验状态/result_run_id。completed 时再核对结果 run 的发起人和 team/group 与来源一致，返回 run_id。读取无写入，不重置退避、租约或模型次数。来源读取不能用本人 Token 替代专用服务鉴别；也不能把内部来源 RPC 暴露给 Gateway。
4. 页面 queued/running 只提示“尚在处理，可稍后重新查看”；exhausted 提示“生成失败，需要人工排查”，不自动重试模型；completed 才使用现有多项草稿 `load(run_id)` 入口，由其再次校验本人和当前范围后逐项审查。无收件记录返回未找到；任一服务不可用返回安全错误，不从缓存猜测状态。浏览器关闭后重新打开仍可从原消息查询，不靠本地存储运行 ID。

生产状态读取复用 worker 已配置的 `TriggerContextClient`：`AGENT_TRIGGER_WORKER_ENABLED=true` 且专用 `AGENT_IM_TRIGGER_*` mTLS 配置齐全时可用。worker 后续故障不关闭普通 Agent RPC 或此客户端；默认关闭时接口返回服务不可用，不宣称后台任务正在执行。本人确认 Task/群回帖规则不变。

## 本批分工和验证

| 角色 | 文件边界 | 小目标 |
| --- | --- | --- |
| 主 agent | `rpc/agent/agent.proto` 与 `pb/agent*.go`、本契约、ADR/计划、共同测试及 Git 整合 | 先固定只读请求/响应；完成后统一测试和审查。 |
| A：Agent | 新 `rpc/agent/trigger_status.go/test.go`、`trigger_status_store.go/test.go`，必要的 `rpc/agent/server.go`、`cmd/agent/main.go` | 当前用户和原作者/团队群双重核对、严格状态读取及生产接线。 |
| B：Gateway | `api/main.go`，新 `api/agent_trigger_status.go/test.go` | 只转发本人 Token 与消息 ID，校验路径范围、状态和字符串 ID。 |
| C：演示页 | `examples/chat.html`、`chat.test.cjs`、`multi-draft-view.js`、`multi-draft-view.test.cjs` | 只在本人已保存指令消息显示查看入口，状态提示与现有集合面板衔接，切换上下文后丢弃旧响应。 |

三个执行 agent 只能在指定独立 worktree 改上述文件，不能修改共同协议、迁移、依赖或文档，不能自行合并 main、推送或调用真实模型。主 agent 集中提交与整合；最多九个独立小步骤按整批计。验证要覆盖他人/离群/失效来源、缺失 inbox、四状态、错范围/非法响应、高于 JS 安全整数 ID、页面重读与上下文切换。真实数据库、生产证书、容器、云仍留最终验收。
