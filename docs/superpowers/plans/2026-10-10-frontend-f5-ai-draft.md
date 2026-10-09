# F5 AI 草稿前端实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在现有 Vue 团队群中完成临时 AI 问答、本人原指令状态、1—5 项草稿人工审查及逐项确认和回帖恢复。

**Architecture:** 群聊仍是入口和上下文；`MessagesPage.vue` 持有临时右侧面板与身份/群范围，`ConversationView.vue` 只发出用户操作。新增 `agent/` 目录分别封装严格解码、HTTP 调用和草稿状态；每次写入以服务端返回或原项重读为准，不在浏览器构造任务结果。

**Tech Stack:** TypeScript 5.9、Vue 3.5、Vite 8、现有 `node --test`；复用 Go Gateway、Agent、Task、IM 接口，不新增产品依赖。

**Spec:** [F5 AI 草稿前端方案](../../frontend-f5-ai-draft-design.md)。关键取舍见[架构记录 F17—F19](../../architecture-decisions.md#前端-f17f19f5-ai-草稿方案用户已确认2026-10-10)；用户已审查并要求继续。

## 全局约束

- 只做 F5；真实 MySQL/Kafka/Redis/mTLS/模型和正式反代链留待 F6 验收。
- 聊天优先；仅团队群顶部问答与本人已保存原指令入口，不建独立 AI 私聊、同步生成按钮、全局运行列表或批量自动确认。
- `run_id`、`revision`、任务与来源 ID 保持十进制字符串；`item_index` 只允许 0—4；`due_at_unix_ms` 是 0—253402300799999 的安全整数。
- 客户端默认 15 秒不变；F5 每个接口的等待长于对应 Gateway 上限，写请求结果不明时先重读同一项。
- 对话、身份、群资格或来源消息变化后废弃旧回包；403/404 清除私有草稿。`accepted` 只表示 IM 已受理。
- 主 agent 统一维护共享契约、共同文档、测试和 Git 集成；如使用执行子 agent，先按协作约定固定各自绝对 worktree、共同提交和唯一允许文件。

## 文件分工

| 文件 | 责任 |
| --- | --- |
| `frontend/src/agent/model.ts`、`model.test.ts` | 原消息资格、状态和 1—5 项集合严格解码、可确认规则 |
| `frontend/src/agent/api.ts`、`api.test.ts` | Ask、状态、集合、逐项读写的路径、请求体及范围复核 |
| `frontend/src/agent/review.ts`、`review.test.ts` | 轮询代际、未保存编辑、逐项冲突与结果不明恢复 |
| `frontend/src/agent/AgentPanel.vue` | Ask 与草稿两种面板视图、逐项表单和可访问反馈 |
| `frontend/src/api/client.ts`、`client.test.ts` | 可选请求超时，原 15 秒默认值保持 |
| `frontend/src/messages/ConversationView.vue` | 当前群顶部入口、持久原消息入口、填入指令聚焦 |
| `frontend/src/messages/MessagesPage.vue` | 面板查询参数、群范围、焦点恢复与关闭清理 |
| `frontend/src/style.css`、`frontend/package.json` | 420px 面板及窄屏覆盖样式、纳入新增测试 |

## 审查重点

1. 历史行缺 `sender_type` 或 `initiator_id`：入口必须隐藏，不能从正文猜测本人可读草稿；任务 1 固定测试。
2. 问答或写操作超过旧客户端 15 秒：按操作等待 Gateway 结果，写入超时后不盲重试；任务 2、6 固定测试。
3. 后台状态返回其他原消息，或集合/单项返回其他运行、团队、群：丢弃数据且不渲染私有内容；草稿来源消息可与原指令不同；任务 3、4 固定测试。
4. 多项中一项成功、一项失败，或版本冲突：仅重读受影响项并保留未保存输入；任务 5、6 固定测试。
5. 离群、换账号、切换指令时旧请求才返回：不得覆盖当前面板，轮询停止；任务 3、7 固定测试。

---

### 任务 1：锁定原消息资格与 Agent 响应解码

**文件：** 新增 `frontend/src/agent/model.ts`、`frontend/src/agent/model.test.ts`；如实际群历史未回传必要字段，由主 agent 先提交接口缺口与取舍，再改后端。

**接口：** 导出 `isOwnAgentCommand(message: ChatMessage, ownId: string): boolean`、`decodeAgentTrigger(value: unknown, scope: { teamId: string; groupId: string; messageId: string }): AgentTrigger | null`、`decodeDraftCollection(value: unknown, scope: AgentScope): DraftCollection | null`、`decodeDraftItem(value: unknown, scope: AgentScope, index: number): DraftItem | null`。`AgentScope` 包含团队、群及从原指令状态取得的运行 ID；草稿项来源消息是独立的讨论依据。

- [ ] 写失败测试：超 2^53 ID 保留字符串、0/1 普通发送者、`initiator_id="0"`、完整 Unicode 空白命令和 2000 码点上限；缺字段、机器人、临时消息、他人消息均拒绝。
- [ ] 运行 `node --test src/agent/model.test.ts`，确认新测试先失败。
- [ ] 实现资格与严格解码：状态仅四种；完成态须正 `run_id`；集合 1—5 项且索引连续；每项状态、版本、负责人/期限依据、回帖状态及团队/群范围须有效。
- [ ] 运行同一命令及 `npm run typecheck`，确认通过；由主 agent 审查并保存这一目标。

### 任务 2：F5 HTTP 路径与按操作超时

**文件：** 新增 `frontend/src/agent/api.ts`、`frontend/src/agent/api.test.ts`；修改 `frontend/src/api/client.ts`、`frontend/src/api/client.test.ts`。

**接口：** `request<T>(path, options?, authenticated?, expectData?, timeoutMs?)` 保持旧调用兼容、默认 15000ms；`createAgentApi(request)` 提供 `ask(teamId, groupId, question)`、`trigger(scope)`、`collection(runId)`、`item(runId, index)`、`editText`、`selectAssignee`、`editDeadline`、`confirm`、`skip`、`retryReply`，所有返回值经过任务 1 解码。

- [ ] 写失败测试：Ask 发送 `{question}`、逐项写入使用服务端字段名、回帖重试空请求体；非十进制 ID/越界索引不发请求；确认 `expected_due_at_unix_ms` 是数字、ID/版本是字符串。
- [ ] 写客户端超时测试：普通调用仍 15 秒；Ask 至少 22 秒、确认至少 20 秒、回帖重试至少 19 秒且留传输余量；身份变化仍抛旧身份错误。
- [ ] 运行 `node --test src/agent/api.test.ts src/api/client.test.ts` 见红，再实现薄 HTTP 适配和可选超时。
- [ ] 重跑两份测试与 `npm run typecheck`；主 agent 审查并保存。

### 任务 3：临时 Ask 和本人指令状态控制

**文件：** 新增 `frontend/src/agent/review.ts`、`frontend/src/agent/review.test.ts`（先实现状态/轮询部分）。

**接口：** `createAgentReview(agentApi, identity, state)` 提供 `openAsk(scope)`、`ask(question)`、`openTrigger(scope)`、`refreshTrigger()`、`close()`、`dispose()`；`state` 区分问答、排队、运行、耗尽、集合读取与错误。

- [ ] 写失败测试：Ask 限 1—2000 个 Unicode 码点且只保留当前面板回答；503/504 保留问题并展示结果未确认；打开指令只用状态接口获得 `run_id`；仅当前面板轮询，5 秒起、2 分钟后 15 秒、10 分钟停并允许手动刷新。
- [ ] 写代际测试：关闭、切群、换账号、403/404、较旧 Ask/状态回包均不能污染新范围；同时只有一个状态请求在途。
- [ ] 运行定向测试见红，再实现状态控制；运行定向测试与类型检查见绿，主 agent 审查并保存。

### 任务 4：集合读取与逐项审查状态

**文件：** 扩展 `frontend/src/agent/review.ts`、`review.test.ts`。

**接口：** `loadCollection()`、`selectItem(index)`、`reloadItem(index)`；集合只从当前 `completed` 状态的 `run_id` 加载，读回运行 ID、团队和群必须与当前范围一致。

- [ ] 写失败测试：1—5 项按待审查/已创建/已跳过计数；部分成功保留各项结果；单项冲突后只重读原项；错误范围和旧世代结果丢弃。
- [ ] 运行定向测试见红，实施最小状态变更；重跑定向测试与类型检查，主 agent 审查并保存。

### 任务 5：逐项编辑与明确负责人、期限选择

**文件：** 扩展 `frontend/src/agent/review.ts`、`review.test.ts`；新增 `frontend/src/agent/AgentPanel.vue` 的草稿审查表单部分。

**接口：** `editText(index, title, description)`、`selectAssignee(index, assigneeId)`、`editDeadline(index, dueAtUnixMs)` 分别使用当前 `revision`；每次成功替换该项服务端保存态，未保存文本留在独立输入态。负责人复用现有当前团队成员分页接口，期限沿用 F4 的 Asia/Shanghai 语义。

- [ ] 写失败测试：未保存编辑禁用确认/跳过，冲突后保留本地输入并重读；`needs_input/not_found/ambiguous/truncated` 要求明确选择，`0` 仅代表本人明确选择未指派或不设期限。
- [ ] 运行定向测试见红；实施状态与表单，切项/关面板时提示保存或放弃。
- [ ] 在表单同时呈现来源消息链接、原始负责人称呼、时间原文/来源/时区/原解析候选与当前保存值，避免把模型依据显示成已确认值。
- [ ] 重跑定向测试和类型检查；主 agent 审查并保存。

### 任务 6：逐项确认、跳过及回帖恢复

**文件：** 扩展 `frontend/src/agent/review.ts`、`review.test.ts`、`AgentPanel.vue`。

**接口：** `confirm(index)` 发送全部当前保存字段与版本，`skip(index)` 发送版本，`retryReply(index)` 仅操作已创建且回帖可重试的原项；F4 任务详情链接使用返回的正任务 ID。

- [ ] 写失败测试：每次仅一项写入；409、503/504、客户端中止先重读该项；`creating` 冻结编辑；成功任务与回帖状态分离；`unknown` 不自动重发；`accepted` 只显示 IM 已受理。
- [ ] 运行定向测试见红；实现最小动作和不确定结果恢复；重跑定向测试与类型检查，主 agent 审查并保存。

### 任务 7：群聊入口、路由和临时右侧面板

**文件：** 修改 `frontend/src/messages/ConversationView.vue`、`MessagesPage.vue`、`frontend/src/style.css`；完成 `frontend/src/agent/AgentPanel.vue`。现有群路由已经接受查询参数，无需为此修改 `frontend/src/router.ts`。

**接口：** 群顶部 `ai` 操作打开 Ask 面板；持久原消息 `ai-trigger` 传正消息 ID；面板路由在当前群使用互斥 `?ai=ask` 或 `?ai_message_id=<id>`，刷新后重查状态；填入指令只更新当前群输入框并聚焦，不发送。

- [ ] 写可测试的路由/入口判定：私聊、未入群、缺历史字段、他人消息和临时气泡均无入口；切群、撤权、换账号清空面板数据和轮询。
- [ ] 实施 Vue 接线及 420px 面板、窄屏覆盖、关闭后焦点恢复、`role=status/alert`、逐项可读按钮名；不改变 F3 发送与已读链。
- [ ] 运行新增定向测试、`npm test`、`npm run build`；主 agent 审查并保存。

### 任务 8：本地组合验收与阶段记录

**文件：** 修改 `frontend/package.json` 纳入新增测试；新增 `docs/frontend-f5-review.md`，更新 `docs/project-plan.md`、`docs/worktree-collaboration-plan.md`；缺口确证时才更新 F5 契约/架构记录。

- [ ] 用 HTTP/WS 替身验证从群聊问答、持久原指令进入、1—5 项部分成功、编辑/确认/跳过/回帖重试和失败恢复；另测两账号权限、切群旧回包及 1280/1440/1920 桌面布局。
- [ ] 运行 `npm test`、`npm run build`、`git diff --check`；如未改 Go 则不把旧 Go 测试当成本轮新验证，如确需后端改动再运行相应 Go 定向及全仓检查。
- [ ] 记录全部实际文件、每一步改动与调用链、具体通过证据及 F6 留存边界；集中审查本分支后保存，不合 main、推送或部署。

## 自查与交接

计划覆盖方案的问答、指令、集合、编辑、确认/跳过、回帖、错误与页面入口；每个任务有独立可审查结果。F17—F19 已确认；若群历史缺字段或 Agent 返回契约与上述不符，暂停受影响实现并更新方案及共同契约。按项目约定，本批最多八个目标，实施时逐项说明“做什么、为什么、解决什么问题、预计改哪些文件”。
