# Task RPC：任务与通知

负责人工或 Agent 确认后的任务创建、团队/本人分页、详情、状态变化、操作记录、个人通知与通知 Outbox。任务来源通过 IM 核验，负责人和操作者资格通过 User 核验，服务不直接读取这两个服务的业务表。

[整体架构](../../docs/architecture.md) · [HTTP 入口](../../api/README.md) · [部署](../../deploy/README.md)

## 当前能力

- [task.proto](task.proto) 提供创建、团队列表、本人开放/已完成分页、详情、状态更新、通知列表与显式已读。
- 管理 `tasks`、`task_operations`、`task_status_notifications`、`task_notification_outbox`。
- 创建请求按操作者和请求键去重：同键同内容返回原任务，同键不同内容冲突；人工和 Agent 创建都遵循此规则。
- 状态为 0 待处理、1 进行中、2 已完成；更新检查权限与可选 `expected_status`，Vue 使用当前读取的状态作为前提。
- 状态、操作、个人通知和 Outbox 在同一事务中保存；实时提醒不替代通知列表，不自动标为已读。
- 有来源 ID 的任务必须核对消息属于当前有权访问的团队群。查询任务返回来源 ID 不授予原消息读取权。

## 本地运行

从仓库根目录运行；先设置 `TASK_MYSQL_DSN`、`USER_RPC_ADDR`、`IM_RPC_ADDR`，准备数据库并启动 User/IM：

```sh
go run ./rpc/task -f rpc/task/etc/task.yaml
go test ./rpc/task
```

默认监听 `127.0.0.1:9003`，Snowflake 节点默认 4，由 `TASK_SNOWFLAKE_NODE_ID` 设置。Compose 地址使用 `user-rpc:9001`、`im-rpc:9002`。

通知发布默认关闭，`docker-compose.notifications.yaml` 显式启用 Task 发布器；Push/WS 的私有 YAML 开关和证书需要同时准备。即使发布关闭，状态更新仍写 Outbox，因此当前数据库表必须齐全。已有库应核对任务/通知迁移及 037 本人分页索引；新库通过完整 `init.sql` 初始化。

## 进一步阅读

[前端任务 API](../../docs/frontend-f4-api-contract.md)、[个人分页索引验证](../../docs/frontend-f4-mysql-explain.md)、[通知 Outbox](../../docs/stage7-notification-outbox-contract.md)、[通知已读](../../docs/stage7-notification-read-state-contract.md)。云端任务与实时通知已测范围见[F6 报告](../../docs/frontend-f6-review.md)。

## 开发过程记录

下面保留早期逐步实现与验证细节，描述当时范围；当前启动方式与能力以上方说明为准。

<details>
<summary>展开历史实现记录</summary>

# 任务 RPC：创建、查询与状态更新

`CreateTask(team_id, title, description, assignee_id, source_group_id, source_message_id, due_at_unix_ms)` 使用 gRPC metadata 中的 `authorization: Bearer <Token>` 和 `idempotency-key`。任务服务先调用用户与团队 RPC 的 `CheckTeamMember` 核对创建者仍在团队；未找到已有同键创建时，如指定 `assignee_id > 0`，再调用 `CheckTeamMemberByID` 核对负责人仍是该团队的可用成员。`assignee_id = 0` 表示暂不指派。两个来源 ID 同时为 0 表示无来源；填写时须同时为正数，任务服务带原 Token 调用 IM 的 `CheckTeamGroupMessage` 核对消息、群、团队归属及当前访问权，校验失败不写任务。`due_at_unix_ms = 0` 表示无截止时间；正数是 UTC Unix 毫秒，最大为 `253402300799999`，直接存为可空整数，避免数据库连接时区改变解释。任务服务只写自己拥有的 `tasks` 表，不读取团队或 IM 表。

标题去首尾空白后须为 1～200 字符，说明最多 2000 字符。创建后状态为待办（`0`）。请求键限 1～64 位 ASCII 字母、数字、`-`、`_`、`.`；同一次创建重试复用键，新创建换键。任务表以 `(creator_id, request_key)` 唯一约束防止并发重复，并保存创建时规范化请求（含来源 ID 和可选截止时间）的摘要，后续编辑标题或说明不会改变重试比较依据。同键同内容返回原任务 ID，同键不同内容返回 `AlreadyExists`。未填写新字段时摘要格式保持兼容；当前团队成员用同一键重试已成功的创建，不重新校验原负责人或来源群的当前资格。

`ListTeamTasks(team_id, after_task_id, limit)` 先通过用户与团队 RPC 核对当前团队资格，再按任务 ID 升序读取该团队任务。`after_task_id=0` 从头开始；默认 20 条、最多 100 条，`next_after_task_id=0` 表示没有下一页。未指派的任务返回 `assignee_id=0`，无来源任务的两个来源 ID 返回 0；未设置截止时间返回 `due_at_unix_ms=0`，否则返回 UTC Unix 毫秒。已有 `(team_id, id)` 索引支持分页查询；来源列需 008 迁移，截止时间列需 009 迁移。

`SetTaskStatus(team_id, task_id, status)` 也先核对当前团队资格；只允许任务创建者、当前负责人或团队拥有者改状态，管理员角色本身不额外授权。状态值为 0 待办、1 进行中、2 完成，当前允许在三态之间切换；重复提交当前状态返回成功且不新增记录。真实变化时，状态更新、`task_operations` 操作记录、给创建者和当前负责人的个人通知以及各自的 `task_notification_outbox` 待发布事件都在同一 MySQL 事务中完成；两人相同只存一条，任一写入失败则整笔回滚。这仅代表通知和事件落库；下述查询/本人显式已读确认分别处理读取和确认，Task事件发布不代表浏览器收到。

`ListTaskNotifications(team_id, before_notification_id, limit)` 先按原 Token 核对当前团队资格并取得本人 ID，再按团队和接收人双条件查询，返回前再次核对资格及同一本人 ID；不接受自报接收人。关联操作记录返回原/新状态、操作者及通知时间，按通知 ID 倒序，默认20条、最大100条，下一页游标为0表示结束。仅列本人记录；离队拒绝，重新入队后原记录可见。此为只读查询，不修改已读状态或发起推送，Gateway 同路径说明见[API文档](../../api/README.md)，协议与边界见[查询契约](../../docs/stage7-notification-read-contract.md)。

A68追加 `read_at_unix_ms`，0为未读，正数为首次本人确认的UTC毫秒。`MarkTaskNotificationRead(team_id,notification_id)` 先查User资格，再按通知ID/团队/本人接收人行锁，空read_at才用数据库CURRENT_TIMESTAMP更新并读回，重复确认不UPDATE；SQL短事务失败回滚，提交不确定同记录重试。提交后资格复核拒绝时可能已经保存已读，不承诺跨服务原子授权或错误必定回滚。统一NotFound不泄露他人记录。028迁移使历史记录默认未读，列表和确认都依赖该列；先迁移再升级Task/Gateway。[共同契约与测试边界](../../docs/stage7-notification-read-state-contract.md)。

Gateway 提供创建、列表和状态更新 HTTP 入口；JSON 契约见 [API 文档](../../api/README.md)。Gateway 与原生页面已接通截止时间及本人通知。首次初始化的数据库使用 `deploy/mysql/init.sql`；已有 `go_im` 数据库须依次执行 `deploy/mysql/migrations/006_tasks.sql`、`007_task_operations.sql`、`008_task_source.sql` 和 `009_task_due_at.sql`，更新 Task RPC 前再依次执行一次 `027_task_status_notifications.sql`、`028_task_notification_read.sql`、`029_task_notification_outbox.sql`，否则状态变化会因通知或Outbox表缺失而回滚，通知查询和已读确认会因新列缺失而失败。即使发布关闭，状态变更也会写Outbox，因此仍须029。本机启动前设置 `TASK_MYSQL_DSN`（指向 `go_im`）、`USER_RPC_ADDR`（例如 `127.0.0.1:9001`）；使用来源消息时另设 `IM_RPC_ADDR`（例如 `127.0.0.1:9002`）并启动 IM RPC，确保用户 RPC 已启动，再执行：

```powershell
go run ./rpc/task -f rpc/task/etc/task.yaml
```

默认 Snowflake 节点号为 `4`，可用 `TASK_SNOWFLAKE_NODE_ID` 覆盖；不同写入进程不能共用节点号。Compose 中任务 RPC 只在容器网络监听 `9003`，并通过 `im-rpc:9002` 校验来源。Gateway 的 `/demo/chat` 已有基础任务列表、创建、状态和来源查看；页面创建表单可填本地截止时间并转成 UTC 毫秒，任务列表按浏览器本地时区显示，经过本地 Node 测试。尚未连接真实 MySQL，也未做容器或浏览器联调。

A69的Task后台发布器默认关闭。启用需 `TASK_NOTIFICATION_PUBLISH_ENABLED=true`、逗号分隔的 `TASK_NOTIFICATION_KAFKA_BROKERS`（如容器网络 `kafka:19092`）、独立 `TASK_NOTIFICATION_TOPIC`（如 `task_notifications`）。Topic须与聊天、Agent触发区分，默认保留名分别为chat_messages/agent_task_triggers；私有配置若改了这两个名称，同时传 `TASK_NOTIFICATION_CHAT_TOPIC`、`TASK_NOTIFICATION_AGENT_TOPIC`。不开启时不构造Kafka writer，但新的待发记录仍留存；029不回填历史通知。每轮读最多100条，只有同步Kafka ACK后才锁定原事实标记发布；失败重建复用同key/payload，可能重复发送。发布循环及数据库不在等待Kafka时持SQL锁，停机取消并等循环退出后关闭writer；published不等于Push消费、浏览器收到或本人已读。Push消费、专用mTLS/WS和页面提醒本批尚未接通，不能把开关设true当整个实时链已完成。[本批契约与验证范围](../../docs/stage7-notification-outbox-contract.md)。

</details>
