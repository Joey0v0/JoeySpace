# F5 任务 2 HTTP 与超时共同契约

日期：2026-10-10。主工作区为 `D:/zy/GoLang/JoeySpace/.worktrees/frontend-f5-ai-draft`。任务 1 模型基线 `e2f08e4`；执行者从主 agent 发布本契约后的共同提交建立独立工作区。完整目标见[八任务计划的任务 2](superpowers/plans/2026-10-10-frontend-f5-ai-draft.md#任务-2f5-http-路径与按操作超时)。

## 本步目的与范围

本步只提供 Agent HTTP 的类型化调用和按操作超时。当前 Vue 请求默认 15 秒，比 Gateway Ask 22 秒、确认 20 秒、回帖重试 19 秒更短，会把仍在运行的调用误报为客户端超时。使用任务 1 严格解码器核对每个响应；不增加页面、轮询、草稿编辑状态或后端接口。

执行 worktree：`D:/zy/GoLang/JoeySpace/.worktrees/frontend-f5-api`，分支 `codex/frontend-f5-api`。执行 agent 仅编辑：

- `frontend/src/agent/api.ts`
- `frontend/src/agent/api.test.ts`
- `frontend/src/api/client.ts`
- `frontend/src/api/client.test.ts`

主 agent 独占共同文档、Git 保存、集成、集中验证和后续任务分配。执行 agent 不自行提交、不改 package 脚本、协议、迁移、依赖、Vue 页面或其他文件，不合 main、推送、部署或派生子 agent。

## 精确调用约束

- `createApiClient().request<T>` 保留现有前四参数与 15000ms 默认值，第五参数允许单次正整数毫秒覆盖；身份代际和原错误映射仍工作。Ask 设 25000ms，确认 23000ms，回帖重试 22000ms，其余 F5 状态/集合/单项读写设 18000ms；均超过各 Gateway 路由的 22/20/19/15 秒上限且有传输余量。仅 F5 调用使用覆盖。
- `createAgentApi(request)` 的触发状态输入包含团队、群、原消息 ID；集合/单项及全部逐项写操作输入均带任务 1 的 `AgentScope {teamId,groupId,runId}`，单项再带 0—4 的 `index`。所有成功响应通过任务 1 的解码器，返回范围与请求不一致时报安全的 502 错误，不能暴露部分回包。
- Ask：`POST /teams/{teamId}/groups/{groupId}/ask`，JSON `{question}`，修剪空白后为 1—2000 个 Unicode 码点；成功数据仅 `{answer}` 且非空。不写聊天、任务或本地历史。
- Trigger：`GET /teams/{teamId}/groups/{groupId}/agent-triggers/{messageId}`。集合：`GET /agent/runs/{runId}/drafts`。单项：`GET /agent/runs/{runId}/drafts/{index}`。
- 文字 PUT 单项路径，体 `{title,description,expected_revision}`；负责人 PUT `/assignee`，体 `{assignee_id,expected_revision}`；期限 PUT `/deadline`，体 `{due_at_unix_ms,expected_revision}`。确认 POST `/confirm` 携带 `expected_title/description/revision/assignee_id/due_at_unix_ms/deadline_resolution`；跳过 POST `/skip` 携带 `{expected_revision}`；回帖 POST `/reply/retry` 无 body、无 query。ID/版本必须是十进制字符串；期限毫秒为 JSON 数字。
- 本步不自动重试任何写请求，也不根据 HTTP 结果推断任务已创建；任务 6 负责结果不明后同一项重读。无效路径参数在 Agent 包装层本地拒绝，不发请求；缺少身份仍沿用现有路由守卫和 Gateway 401，不改变通用 `client.request` 的既有认证行为。

## 验证

按 TDD 先新增失败测试，再实现；运行 `node --test src/agent/api.test.ts src/api/client.test.ts` 和 `npm run typecheck`，记录红绿证据。主 agent 复核后另行安排只读规格/质量审查。本步的 `api.test.ts` 暂由定向命令运行，任务 8 再纳入 `npm test`。
