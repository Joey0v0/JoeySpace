# F5 AI 草稿前端本地审查（2026-10-10）

分支 `codex/frontend-f5-ai-draft`，基线 `6a4aaaf`。用户已确认 [F5 设计](frontend-f5-ai-draft-design.md)、[F17—F19](architecture-decisions.md#前端-f17f19f5-ai-草稿方案用户已确认2026-10-10) 和 [八任务计划](superpowers/plans/2026-10-10-frontend-f5-ai-draft.md)。本记录描述隔离分支的本地实现与验收，不代表已合入 `main` 或完成真实服务环境验收。

## 八步结果与调用链

1. **本人指令和严格解码。** 仅已加入的团队群中、本人已持久化的普通文本 `@AI 整理任务 <要求>` 消息显示处理入口。Agent 状态、运行集合和草稿项均核对完整范围、字段和字符串 ID；讨论来源消息 ID 不误当作触发消息 ID。
2. **按操作超时。** Vue Agent API 复用 Bearer 客户端，为 Ask、状态/集合/项读取、写入、确认和回帖重试分别设置覆盖 Gateway 上限的等待；请求返回先严格解码，不改已有 F2—F4 的默认超时。
3. **临时问答与原指令状态。** 当前群顶部 Ask 只在面板显示回答；本人原消息按消息 ID 查询 `queued/running/exhausted/completed`，仅当前打开的指令轮询，刷新可重查；账号、群或权限变化清除旧状态和在途结果。
4. **多项集合。** `completed` 的服务端 `run_id` 才可读取 1—5 项集合。逐项重读只替换对应项，计算待审查/已创建/已跳过数量；跨运行、跨群、跨账号的旧回包不能覆盖当前面板。
5. **逐项编辑。** 标题/说明、负责人、期限分开保存，均携带当前 `revision`。面板展示负责人匹配、原时间与来源，待确认项的含糊字段须明确补正；409 或不确定写入先重读本项，未保存文字保留供本人比对。
6. **单项确认与回帖。** 确认携带全部服务端已保存字段和版本；跳过只影响当前项。`creating` 先重读才允许显式继续；任务已创建与群回帖分别展示，`not_started/pending` 先重读再重试，`unknown` 不自动重发，`accepted` 只表示 IM 受理。
7. **群聊接线。** 当前群查询参数 `?ai=ask` 与 `?ai_message_id=<id>` 互斥；刷新重查状态。填入指令仅更新群输入框并聚焦；420px 面板在桌面并排、较窄视口覆盖聊天，关闭后恢复入口焦点。F3 发送与已读链未改。
8. **组合验收。** 新增 Agent 四组测试纳入 `npm test`。本地 Chrome 配合 HTTP/WS 替身从群聊 Ask 走到五项草稿的编辑、确认、跳过和回帖重试；记录下述证据与真实环境边界。

## 已执行验证

- `npm test`：F5 新增 37 项与原有 95 项，合计 132/132 通过；`npm run build` 含 `vue-tsc --noEmit` 通过；`git diff --check` 通过。
- 本地 Chrome 的第一条脚本走通顶部 Ask、填入但不发送、本人原消息状态、深链刷新、关闭后焦点恢复、路由切换；900/1280/1440/1920px 检查无横向溢出。
- 第二条组合脚本用五项草稿验证从 Ask 到原指令审查：初始待审查 3、已创建 1、已跳过 1；文字保存首遇 409 后重读并保留输入，再保存、确认第 1 项，重读回帖并显式重试；跳过第 2 项；第 3 项先补负责人和不设期限再确认；最终待审查 0、已创建 3、已跳过 2。未加入群无入口，另一账号无法看到本人原消息入口且其深链收到 403 后清空面板；切离群时旧状态回包不会重现。审查面板在 900/1280/1440/1920px 均为 420px、无横向溢出。测试脚本、JSON 请求证据和截图保存在忽略目录 `.superpowers/sdd/2026-10-10-frontend-f5-ai-draft/`，没有增加项目依赖。

## 验证边界与后续

浏览器使用本地 HTTP/WS 替身；它验证前端操作、状态和布局，不证明真实 Agent 模型质量、Gateway/Agent/Task/IM 联动、MySQL/Redis/Kafka/mTLS 或机器人回帖送达。另一账号与撤权仍需在真实服务和两账号浏览器中复核；本地模拟的 403 只验证前端清理行为。F6 继续在真实环境验收完整前端链，模型间歇失败与服务恢复按实际结果记录。`main` 未合并，本分支未推送或部署。

## 全部实际修改文件

以下为基线 `6a4aaaf` 至 F5 本地实现的全部跟踪文件；忽略目录的验收脚本和截图另见上节。

- [docs/architecture-decisions.md](architecture-decisions.md)
- [docs/frontend-f5-ai-draft-design.md](frontend-f5-ai-draft-design.md)
- [docs/frontend-f5-task1-contract.md](frontend-f5-task1-contract.md)
- [docs/frontend-f5-task2-contract.md](frontend-f5-task2-contract.md)
- [docs/frontend-f5-task3-contract.md](frontend-f5-task3-contract.md)
- [docs/frontend-f5-review.md](frontend-f5-review.md)
- [docs/project-plan.md](project-plan.md)
- [docs/superpowers/plans/2026-10-10-frontend-f5-ai-draft.md](superpowers/plans/2026-10-10-frontend-f5-ai-draft.md)
- [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md)
- [frontend/package.json](../frontend/package.json)
- [frontend/src/agent/AgentPanel.vue](../frontend/src/agent/AgentPanel.vue)
- [frontend/src/agent/api.test.ts](../frontend/src/agent/api.test.ts)
- [frontend/src/agent/api.ts](../frontend/src/agent/api.ts)
- [frontend/src/agent/model.test.ts](../frontend/src/agent/model.test.ts)
- [frontend/src/agent/model.ts](../frontend/src/agent/model.ts)
- [frontend/src/agent/navigation.test.ts](../frontend/src/agent/navigation.test.ts)
- [frontend/src/agent/navigation.ts](../frontend/src/agent/navigation.ts)
- [frontend/src/agent/review.test.ts](../frontend/src/agent/review.test.ts)
- [frontend/src/agent/review.ts](../frontend/src/agent/review.ts)
- [frontend/src/api/client.test.ts](../frontend/src/api/client.test.ts)
- [frontend/src/api/client.ts](../frontend/src/api/client.ts)
- [frontend/src/messages/ConversationView.vue](../frontend/src/messages/ConversationView.vue)
- [frontend/src/messages/MessagesPage.vue](../frontend/src/messages/MessagesPage.vue)
- [frontend/src/style.css](../frontend/src/style.css)
