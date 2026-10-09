# JoeySpace F4 任务与通知 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox syntax for tracking.

**Goal:** 在 F3 真实聊天基线上交付跨团队本人任务、任务详情与创建、并发安全三态、来源定位和持久通知工作区。

**Architecture:** Task 持有跨团队本人任务和通知事实，IM 持有来源消息上下文，User 持有团队资格与成员显示资料，Gateway 只组合当前页读模型。Vue 的 WebSocket 只提供刷新信号，再读取权威 HTTP 数据。

**Tech Stack:** Go、go-zero、gRPC、GORM/MySQL 8.0、TypeScript、Vue 3、Vue Router、Vite、Node test runner。

**Spec:** docs/frontend-f4-tasks-design.md

## Global Constraints

- 桌面浏览器优先；任务详情和新建面板宽 380—440px，窄于 1100px 时覆盖列表。
- 默认只显示仍有团队资格且分配给本人的待办/进行中任务；已完成任务单独读取；浏览器不得逐团队聚合。
- 浏览器可见 ID 全部使用十进制字符串；RPC 与数据库内部继续使用 int64。
- 状态写入携带 expected_status；同目标状态重试成功，其他并发变化返回 gRPC Aborted / HTTP 409。
- 创建结果不确定时冻结规范化请求并复用同一幂等键；来源只允许已持久化的团队群消息。
- 来源上下文最多目标前 20 条、后 20 条，按消息 ID 升序返回，并复用现有团队、群、代际和提及权限。
- 通知事实和权威未读数来自 Task；task_notification_changed 只触发串行刷新。
- 不增加服务、框架或 UI 库；不实现任务编辑、看板、拖拽、优先级、子任务、评论、附件、批量操作、通知归档、系统通知或 F5。
- 主 agent 独占协议、生成代码、迁移和共同文档；执行 agent 不修改这些文件，不合 main、不推送、不部署。

## Review Focus

- Task 6、8 覆盖账号、路由、筛选变化后旧请求和旧 WebSocket 回调不能污染新状态。
- Task 1、3、5、6、8 覆盖超过 JavaScript 安全整数的所有 ID。
- Task 2、3、5 覆盖离队或撤权后任务、详情、通知和来源正文都不返回。
- Task 4、7 覆盖创建或状态超时后先重读权威详情。
- Task 6、8 覆盖分页失败保留已加载内容，通知刷新失败保留提示圆点。

---

### Task 1: 固定 F12—F16 RPC 与 HTTP 契约

**Files:**
- Modify: rpc/task/task.proto, rpc/task/pb/task.pb.go, rpc/task/pb/task_grpc.pb.go
- Modify: rpc/im/im.proto, rpc/im/pb/im.pb.go, rpc/im/pb/im_grpc.pb.go
- Modify: rpc/user/user.proto, rpc/user/pb/user.pb.go, rpc/user/pb/user_grpc.pb.go
- Create: rpc/task/f4_contract_test.go, rpc/im/source_context_contract_test.go, rpc/user/task_display_names_contract_test.go
- Create: docs/frontend-f4-api-contract.md
- Modify: api/handler.go
- Modify: docs/frontend-f4-tasks-design.md, docs/architecture-decisions.md, docs/project-plan.md, docs/worktree-collaboration-plan.md

**Interfaces:**
- Produces: Task.ListMyTasks, Task.GetTask, Task.ListMyTaskNotifications, TaskItem.team_id, optional SetTaskStatusRequest.expected_status, IM.GetTeamGroupMessageContext, User.BatchGetTeamMemberDisplayNames, User.BatchGetMyTeamNames.
- Produces HTTP contract: GET /api/v1/tasks, GET /api/v1/teams/{team_id}/tasks/{task_id}, GET /api/v1/task-notifications, GET /api/v1/teams/{team_id}/groups/{group_id}/messages/{message_id}/context.

- [ ] Write descriptor tests for exact RPC names, field numbers/presence, cursor fields, 41-message context cap and 100-ID name limits; run before generation and capture expected failure.
- [ ] Add backward-compatible protobuf numbers and regenerate with repository protoc 31.1 and pinned Go plugins.
- [ ] Document validation, cursor binding, sort order, errors, JSON string IDs, context order and name fallback.
- [ ] Mark F12—F16 user-confirmed and record execution boundaries.
- [ ] Run go test ./rpc/task ./rpc/im ./rpc/user ./api -count=1; expected PASS, then commit.

### Task 2: Task 本人任务、详情与查询索引

**Files:**
- Create: rpc/task/my_tasks.go, my_tasks_test.go, detail.go, detail_test.go, cursor.go, cursor_test.go
- Modify: rpc/task/create.go, rpc/task/main.go, rpc/task/server_test.go
- Modify: deploy/mysql/init.sql, deploy/README.md
- Create: deploy/mysql/migrations/037_task_personal_indexes.sql, deploy/mysql/migrations/037_task_personal_indexes_test.go

**Interfaces:**
- Consumes Task 1 types and separate existing User.GetMyInfo, User.ListMyTeams and teamChecker capabilities.
- Produces a versioned base64url cursor bound to view and optional team; open order is due bucket, due_at, task_id; completed order is task_id descending; GetTask returns can_update_status.

- [ ] Write failing tests for multi-team membership, team filter, open/completed order, invalid or reused cursor, large IDs, departure, detail permission, DB failure and can_update_status.
- [ ] Implement strict cursor parsing and minimum queries; first page fixes snapshot_now_unix_ms and snapshot_upper_task_id.
- [ ] Capture focused RED and GREEN Task test output.
- [ ] Run EXPLAIN FORMAT=JSON on MySQL 8.0 for final SQL before and after candidate indexes. Without MySQL 8.0, record this substep pending and do not invent evidence.
- [ ] Keep only EXPLAIN-supported indexes in 037 and init.sql; run DDL consistency and go test ./rpc/task -count=1, then commit.

### Task 3: User 显示名与 Gateway 任务读模型

**Files:**
- Create: rpc/user/task_display_names.go, task_display_names_test.go, my_team_names.go, my_team_names_test.go
- Create: api/my_tasks.go, my_tasks_test.go, task_detail.go, task_detail_test.go, task_enrichment.go, task_enrichment_test.go
- Modify: api/main.go

**Interfaces:**
- Consumes Task 1 list/detail and User bounded batch names.
- Produces team_name, creator_name and assignee_name for only the current result page; missing profiles use empty names, service failures remain errors.

- [ ] Write failing User tests for caller membership, active targets, dedupe, max 100 IDs and omitted missing data.
- [ ] Implement internal User batch lookups without public arbitrary-name HTTP.
- [ ] Write failing Gateway tests for string IDs, cursor forwarding, bounded fan-out, metadata, name fallback and errors.
- [ ] Implement GET handlers/routes; run go test ./rpc/user ./api -count=1, then commit.

### Task 4: 状态并发前提与创建 HTTP 契约

**Files:**
- Modify: rpc/task/status.go, rpc/task/status_test.go
- Modify: api/task_status.go, api/task_status_test.go, api/task_create.go, api/task_create_test.go
- Modify: examples/chat.html, examples/chat_test.go

**Interfaces:**
- expected_status uses proto3 optional presence and is required by Task; same target status is idempotent success; differing current/expected status is Aborted and HTTP 409. The legacy demo sends its displayed status; create keeps one Idempotency-Key.

- [ ] Write failing Task tests for locked comparison, replay, conflict rollback and no operation/notification/outbox on conflict.
- [ ] Implement comparison inside the existing transaction.
- [ ] Write failing Gateway tests requiring integer expected_status, mapping Aborted and preserving create validation/idempotency.
- [ ] Update the demo caller and run go test ./rpc/task ./api ./examples -count=1, then commit.

### Task 5: IM 来源上下文与 Gateway 路由

**Files:**
- Create: rpc/im/team_group_message_context.go, team_group_message_context_test.go
- Create: api/team_group_message_context.go, team_group_message_context_test.go
- Modify: api/main.go

**Interfaces:**
- Produces target plus at most 20 earlier and 20 later messages in ascending ID order with sender, initiator and mention fields; Gateway returns string IDs.

- [ ] Write failing IM tests for boundaries, missing target, wrong group, departure, generation change, bot fields, mentions and DB failures.
- [ ] Implement a bounded query around the target after existing authorization and final authorization recheck.
- [ ] Write failing Gateway transport/error tests and register the route.
- [ ] Run go test ./rpc/im ./api -count=1, then commit.

### Task 6: Vue 任务列表、分页和详情面板

**Files:**
- Create: frontend/src/tasks/model.ts, model.test.ts, workspace.ts, workspace.test.ts, TasksPage.vue, TaskList.vue, TaskDetailPanel.vue
- Modify: frontend/src/api/client.ts, client.test.ts, frontend/src/router.ts, frontend/src/App.vue, frontend/src/style.css, frontend/package.json
- Delete: frontend/src/TaskPreview.vue

**Interfaces:**
- Produces /tasks and /tasks/teams/:teamId/:taskId, filters, grouping, pagination, retained-content retry and a 380—440px detail panel.

- [ ] Write failing model/API tests for string IDs, grouping, cursor preservation, stale responses, 401/403 clearing and continuation failure.
- [ ] Implement the smallest task state model and strict decoders.
- [ ] Replace preview with list/detail components, semantic labels, focus and text status.
- [ ] Run npm test and npm run build, then commit.

### Task 7: Vue 创建、状态恢复与来源往返

**Files:**
- Create: frontend/src/tasks/mutations.ts, mutations.test.ts, create.ts, create.test.ts, TaskFormPanel.vue
- Create: frontend/src/messages/sourceContext.ts, sourceContext.test.ts
- Modify: frontend/src/tasks/model.ts, model.test.ts, TaskDetailPanel.vue
- Modify: frontend/src/messages/ConversationView.vue, MessagesPage.vue, frontend/src/router.ts, frontend/src/style.css, frontend/package.json

**Interfaces:**
- Produces frozen create attempts with one request key, endpoint-specific 409 text, timeout detail recheck, persisted group-message action, focus_message_id context and browser-back return.

- [ ] Write failing tests for key reuse, frozen form, assignment, Shanghai-to-UTC conversion, status timeout branches, private/unpersisted source, task-specific 409 and revoked source.
- [ ] Implement creation and status state transitions before controls.
- [ ] Add one action to persisted group messages and source context highlight without arbitrary returnTo.
- [ ] Run npm test and npm run build, then commit.

### Task 8: 跨团队通知、显式已读与实时提示

**Files:**
- Create: rpc/task/my_notifications.go, my_notifications_test.go
- Create: api/my_task_notifications.go, my_task_notifications_test.go
- Create: frontend/src/tasks/notifications.ts, notifications.test.ts, TaskNotifications.vue
- Create: frontend/src/realtime/taskSignal.ts, taskSignal.test.ts
- Modify: frontend/src/realtime/client.ts, client.test.ts, frontend/src/tasks/TasksPage.vue, frontend/src/messages/MessagesPage.vue, frontend/src/App.vue, frontend/src/style.css, frontend/package.json, api/main.go

**Interfaces:**
- Produces unread-first partition cursor and authoritative unread_count. Explicit read refreshes from the first page. Pages own one route-local WebSocket; a shared boolean signal drives the nav dot and serialized refresh.

- [ ] Write failing Task tests for memberships, unread-first order, cursor, unread count, task-title/current-status join, departure and DB failures.
- [ ] Implement Task and Gateway composition with strict IDs and errors.
- [ ] Write failing frontend tests for frame parsing, dedupe, one queued refresh, reconnect, explicit read, failure retention and identity cleanup.
- [ ] Implement notification tab, read action, route-local client and nav dot; run Go tests, npm test and npm run build, then commit.

### Task 9: 集成、浏览器替身与集中审查

**Files:**
- Create: api/frontend_f4_flow_test.go, docs/frontend-f4-review.md
- Modify: docs/project-plan.md, docs/worktree-collaboration-plan.md
- Test only: all Task 1—8 changes

**Interfaces:** Produces review evidence and explicit real-environment gaps; no product feature.

- [ ] Run actual HTTP→TCP gRPC compositions for list/detail/status/context/notifications with large IDs, metadata and failures.
- [ ] Run go test ./... -count=1, npm test, npm run build, migration consistency and any available MySQL 8.0 EXPLAIN.
- [ ] Exercise login → messages → task → status → source → notification/read with browser substitutes at 1280, 1440 and 1920 widths and keyboard focus.
- [ ] Complete per-task and whole-branch review; fix all Critical/Important findings.
- [ ] Record files, call chains, results and unverified MySQL/Kafka/mTLS/browser/deployment items; update only verified progress, then commit.
