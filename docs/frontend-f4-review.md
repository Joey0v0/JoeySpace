# F4 任务与通知本地审查（2026-10-09）

分支 `codex/frontend-f4-tasks`；基线 `69bb016`。F4 是前端第四阶段，F12—F16 是本阶段的架构决定编号。用户已审查并确认[任务与通知设计](frontend-f4-tasks-design.md)，本批按[九步计划](superpowers/plans/2026-10-09-frontend-f4-tasks.md)实施。以下结果只代表本地隔离分支，不代表已合入 `main` 或完成云端验收。

## 九步结果与主要调用链

1. **共同契约。** 固定 Task 本人任务/详情/通知、IM 来源上下文、User 有限补名及状态前提字段；Gateway 的大整数 ID、错误映射见[API 契约](frontend-f4-api-contract.md)。解决服务间字段和权限责任不一致。
2. **本人任务查询。** Task 用 Bearer 确认本人，通过 User 活动团队资格约束 MySQL 查询；待处理按截止时间分组、已完成独立分页，详情重新核权。10 万条合成数据的 MySQL 8.0.46 `EXPLAIN` 支持只加 037 复合索引，具体结果见[执行计划](frontend-f4-mysql-explain.md)。
3. **有限补名与 Gateway。** User 仅向当前有资格的人返回请求页涉及的团队/成员名称；Gateway 从 Task 读取后向 User 按页补名，并把浏览器可见 ID 编码成字符串。下游失败不能变成空列表。
4. **状态前提。** Vue 提交 `status` 和 `expected_status`；Gateway 原样传 Task；Task 在原有行锁事务内核对权限和旧状态，冲突返回 409，不写操作、通知或 Outbox。同目标重试仍幂等。
5. **来源上下文。** Gateway→IM 按团队、群、消息 ID 读取目标前后各最多 20 条；IM 在读前后复核当前资格和代际，Vue 从任务详情回到准确的群消息并高亮目标。
6. **任务工作区。** Vue 的“我的任务”在桌面列表中显示本人开放/已完成任务、团队筛选、续页、详情和失败重试；账号或范围变化会丢弃旧响应。
7. **创建与处理。** Vue 在右侧面板创建任务，来源消息可预填；不确定的创建结果复用冻结请求和原幂等键恢复。状态请求超时后重读详情再判断成功、冲突或可重试。
8. **持久通知。** Task 跨当前活动团队读取本人通知、未读优先并返回权威未读数；Gateway 补当前页团队/操作者名称。Vue 在任务页签中逐条显式已读、刷新第一页；WS 只发重读提示，模块导航只保留提醒圆点。
9. **集中集成。** 新增 `api/frontend_f4_flow_test.go`，让 HTTP 处理器实际走本机 TCP gRPC 到 Task、User、IM 替身服务，覆盖列表、详情、状态、来源上下文、通知列表和已读；断言 Bearer、大整数 ID、状态 409、无权 403、缺项 404、缺少前提 400 和错误信息不泄漏。

## 已执行验证

- `go test ./api -run TestF4HTTPThroughTCPGRPCTaskContextAndNotification -count=1` 通过；`go test ./... -count=1` 全仓通过。
- 前端 `npm test` 90/90 通过；`npm run build` 中 TypeScript 检查和 Vite 生产构建通过。
- `go test ./deploy/mysql/migrations -run TestPersonalTaskIndexInitializationMatchesUpgrade -count=1` 通过：037 与 `init.sql` 的索引定义一致；`git diff --check` 通过。
- F4-2 已在临时 MySQL 8.0.46 的 10 万条合成任务上执行最终 SQL 的 `EXPLAIN`。F4-9 检查时 Docker 引擎未运行，本轮未再次运行数据库查询或迁移。
- 本机无界面 Chrome 使用本地 API/WS 替身完成登录→消息→任务列表/详情→状态 PUT 与详情复核→来源群上下文→通知读取和显式已读。1280×720、1440×900、1920×1080 的详情/通知截图均无横向溢出；Tab 可抵达任务行，焦点轮廓可见。请求日志与截图保存在忽略目录 `.superpowers/sdd/2026-10-09-frontend-f4-tasks/` 的 `browser-evidence.json` 和六张 PNG；脚本为同目录 `browser-check.cjs`，没有增加项目依赖。浏览器脚本没有覆盖物理键盘输入法或真实 WS 投递。

## 独立审查与修复

独立只读全分支审查发现两项 Important，均先补失败回归用例，再修复并复核通过：

- 创建 POST 返回 503 时保留冻结请求与原幂等键；服务端可能已经提交，页面再次核对须按原键重放，不能生成第二个任务。
- 当前团队的任务/通知列表刷新返回 403/404 时清除旧私有行、游标和相关详情；临时 503 仍保留已加载内容。撤权清理同时推进请求世代，防止旧在途结果把内容放回页面。

三条新增用例修复前失败；修复后定向 22/22 及前端构建通过。审查者复核六个受影响文件，没有剩余 Important 发现。

## 验证边界

HTTP 集成使用真实本机 TCP gRPC 传输和服务替身；Task、User、IM 的生产处理器另由各自包测试覆盖，不能把这项组合称为真实 MySQL 或跨进程服务部署。Chrome 使用本地替身数据，证明页面流程和桌面布局，不证明生产后端链。037 尚未在目标数据卷执行；已有的合成数据执行计划不代表生产分布、DDL 锁等待或延迟。真实 Kafka/WebSocket 投递、Redis 票据、mTLS、双账号群聊浏览器操作、物理键盘与云端部署仍需在后续环境验收。

## 全部实际修改文件

以下链接按 `69bb016` 至本批最终提交列出；生成代码和原生演示页兼容改动也列在其中。

- [api/frontend_f4_flow_test.go](../api/frontend_f4_flow_test.go)
- [api/handler.go](../api/handler.go)
- [api/main.go](../api/main.go)
- [api/my_task_notifications_test.go](../api/my_task_notifications_test.go)
- [api/my_task_notifications.go](../api/my_task_notifications.go)
- [api/my_tasks_test.go](../api/my_tasks_test.go)
- [api/my_tasks.go](../api/my_tasks.go)
- [api/task_create_test.go](../api/task_create_test.go)
- [api/task_detail_test.go](../api/task_detail_test.go)
- [api/task_detail.go](../api/task_detail.go)
- [api/task_enrichment_test.go](../api/task_enrichment_test.go)
- [api/task_enrichment.go](../api/task_enrichment.go)
- [api/task_notification_read_test.go](../api/task_notification_read_test.go)
- [api/task_notification_read.go](../api/task_notification_read.go)
- [api/task_status_test.go](../api/task_status_test.go)
- [api/task_status.go](../api/task_status.go)
- [api/team_group_message_context_test.go](../api/team_group_message_context_test.go)
- [api/team_group_message_context.go](../api/team_group_message_context.go)
- [deploy/mysql/init.sql](../deploy/mysql/init.sql)
- [deploy/mysql/migrations/037_task_personal_indexes_test.go](../deploy/mysql/migrations/037_task_personal_indexes_test.go)
- [deploy/mysql/migrations/037_task_personal_indexes.sql](../deploy/mysql/migrations/037_task_personal_indexes.sql)
- [deploy/README.md](../deploy/README.md)
- [deploy/verify-cloud.py](../deploy/verify-cloud.py)
- [docs/architecture-decisions.md](../docs/architecture-decisions.md)
- [docs/frontend-f4-api-contract.md](../docs/frontend-f4-api-contract.md)
- [docs/frontend-f4-mysql-explain.md](../docs/frontend-f4-mysql-explain.md)
- [docs/frontend-f4-review.md](../docs/frontend-f4-review.md)
- [docs/frontend-f4-tasks-design.md](../docs/frontend-f4-tasks-design.md)
- [docs/project-plan.md](../docs/project-plan.md)
- [docs/superpowers/plans/2026-10-09-frontend-f4-tasks.md](../docs/superpowers/plans/2026-10-09-frontend-f4-tasks.md)
- [docs/worktree-collaboration-plan.md](../docs/worktree-collaboration-plan.md)
- [examples/chat.html](../examples/chat.html)
- [examples/chat.test.cjs](../examples/chat.test.cjs)
- [frontend/package.json](../frontend/package.json)
- [frontend/src/api/client.test.ts](../frontend/src/api/client.test.ts)
- [frontend/src/api/client.ts](../frontend/src/api/client.ts)
- [frontend/src/App.vue](../frontend/src/App.vue)
- [frontend/src/messages/ConversationView.vue](../frontend/src/messages/ConversationView.vue)
- [frontend/src/messages/MessagesPage.vue](../frontend/src/messages/MessagesPage.vue)
- [frontend/src/messages/sourceContext.test.ts](../frontend/src/messages/sourceContext.test.ts)
- [frontend/src/messages/sourceContext.ts](../frontend/src/messages/sourceContext.ts)
- [frontend/src/realtime/client.test.ts](../frontend/src/realtime/client.test.ts)
- [frontend/src/realtime/client.ts](../frontend/src/realtime/client.ts)
- [frontend/src/realtime/taskSignal.test.ts](../frontend/src/realtime/taskSignal.test.ts)
- [frontend/src/realtime/taskSignal.ts](../frontend/src/realtime/taskSignal.ts)
- [frontend/src/router.ts](../frontend/src/router.ts)
- [frontend/src/style.css](../frontend/src/style.css)
- [frontend/src/TaskPreview.vue](../frontend/src/TaskPreview.vue)
- [frontend/src/tasks/create.test.ts](../frontend/src/tasks/create.test.ts)
- [frontend/src/tasks/create.ts](../frontend/src/tasks/create.ts)
- [frontend/src/tasks/model.test.ts](../frontend/src/tasks/model.test.ts)
- [frontend/src/tasks/model.ts](../frontend/src/tasks/model.ts)
- [frontend/src/tasks/mutations.test.ts](../frontend/src/tasks/mutations.test.ts)
- [frontend/src/tasks/mutations.ts](../frontend/src/tasks/mutations.ts)
- [frontend/src/tasks/notifications.test.ts](../frontend/src/tasks/notifications.test.ts)
- [frontend/src/tasks/notifications.ts](../frontend/src/tasks/notifications.ts)
- [frontend/src/tasks/TaskDetailPanel.vue](../frontend/src/tasks/TaskDetailPanel.vue)
- [frontend/src/tasks/TaskFormPanel.vue](../frontend/src/tasks/TaskFormPanel.vue)
- [frontend/src/tasks/TaskList.vue](../frontend/src/tasks/TaskList.vue)
- [frontend/src/tasks/TaskNotifications.vue](../frontend/src/tasks/TaskNotifications.vue)
- [frontend/src/tasks/TasksPage.vue](../frontend/src/tasks/TasksPage.vue)
- [frontend/src/tasks/workspace.test.ts](../frontend/src/tasks/workspace.test.ts)
- [frontend/src/tasks/workspace.ts](../frontend/src/tasks/workspace.ts)
- [rpc/im/im.proto](../rpc/im/im.proto)
- [rpc/im/pb/im_grpc.pb.go](../rpc/im/pb/im_grpc.pb.go)
- [rpc/im/pb/im.pb.go](../rpc/im/pb/im.pb.go)
- [rpc/im/source_context_contract_test.go](../rpc/im/source_context_contract_test.go)
- [rpc/im/team_group_message_context_test.go](../rpc/im/team_group_message_context_test.go)
- [rpc/im/team_group_message_context.go](../rpc/im/team_group_message_context.go)
- [rpc/task/create.go](../rpc/task/create.go)
- [rpc/task/cursor_test.go](../rpc/task/cursor_test.go)
- [rpc/task/cursor.go](../rpc/task/cursor.go)
- [rpc/task/detail_test.go](../rpc/task/detail_test.go)
- [rpc/task/detail.go](../rpc/task/detail.go)
- [rpc/task/f4_contract_test.go](../rpc/task/f4_contract_test.go)
- [rpc/task/main.go](../rpc/task/main.go)
- [rpc/task/my_notifications_test.go](../rpc/task/my_notifications_test.go)
- [rpc/task/my_notifications.go](../rpc/task/my_notifications.go)
- [rpc/task/my_tasks_test.go](../rpc/task/my_tasks_test.go)
- [rpc/task/my_tasks.go](../rpc/task/my_tasks.go)
- [rpc/task/notification_cursor.go](../rpc/task/notification_cursor.go)
- [rpc/task/pb/task_grpc.pb.go](../rpc/task/pb/task_grpc.pb.go)
- [rpc/task/pb/task.pb.go](../rpc/task/pb/task.pb.go)
- [rpc/task/server_test.go](../rpc/task/server_test.go)
- [rpc/task/status_test.go](../rpc/task/status_test.go)
- [rpc/task/status.go](../rpc/task/status.go)
- [rpc/task/task.proto](../rpc/task/task.proto)
- [rpc/user/my_team_names_test.go](../rpc/user/my_team_names_test.go)
- [rpc/user/my_team_names.go](../rpc/user/my_team_names.go)
- [rpc/user/pb/user_grpc.pb.go](../rpc/user/pb/user_grpc.pb.go)
- [rpc/user/pb/user.pb.go](../rpc/user/pb/user.pb.go)
- [rpc/user/task_display_names_contract_test.go](../rpc/user/task_display_names_contract_test.go)
- [rpc/user/task_display_names_test.go](../rpc/user/task_display_names_test.go)
- [rpc/user/task_display_names.go](../rpc/user/task_display_names.go)
- [rpc/user/user.proto](../rpc/user/user.proto)
