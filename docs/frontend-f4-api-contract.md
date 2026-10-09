# F4 任务与通知接口契约

更新日期：2026-10-09  
状态：F12—F16 已由用户确认；本文固定 F4 各执行任务共享的 RPC/HTTP 边界，不表示业务实现已经完成。

## 1. 共同规则

- 浏览器可见的 team_id、task_id、notification_id、actor_id、user_id、group_id、message_id、时间戳和 unread_count 均编码为十进制字符串。布尔值、状态值和分页 limit 保持 JSON 布尔或数字。
- HTTP Bearer 原样转发为 gRPC authorization metadata；本人身份只能由服务端解析，不接受 user_id 查询参数。
- limit 省略或为 0 时使用 20，最大 50。负数、超过上限、非法 ID、未知 view 或非法游标返回 gRPC InvalidArgument / HTTP 400。
- 下游 Unauthenticated、PermissionDenied、NotFound、Aborted、Unavailable、DeadlineExceeded 分别映射 HTTP 401、403、404、409、503、504；其他内部错误映射 502，响应不泄漏数据库或 RPC 地址。
- Task、IM 或 User 失败不能伪装成成功空结果。显示名缺失可以中性留空，整个补名服务失败仍返回错误。
- 列表后续页失败保留浏览器已经加载的内容与原游标；刷新从空游标重新读取权威范围。

## 2. 本人任务

RPC：Task.ListMyTasks

请求：
- view：0 为待处理，只含待办和进行中；1 为已完成。
- team_id：0 为全部当前活动团队；正数为单团队筛选。
- cursor：首次为空，后续原样回传。
- limit：默认 20，最大 50。

Task 用 authorization 得到本人，并以 User 当前活动团队资格约束查询。第一页固定 snapshot_now_unix_ms 与 snapshot_upper_task_id；游标是版本化 base64url 文本，并绑定 view 和 team_id。游标结构不构成外部 API，服务端严格校验版本、范围和筛选一致性。

待处理顺序为已逾期、未来七天、更晚、无截止时间；组内按 due_at_unix_ms、task_id 升序。已完成按 task_id 倒序。响应 TaskItem 增加 team_id，next_cursor 为空表示结束。

HTTP：GET /api/v1/tasks?view=open|completed&team_id={id}&cursor={cursor}&limit={n}

Gateway 只为当前页调用 User.BatchGetMyTeamNames 和按团队调用 User.BatchGetTeamMemberDisplayNames，补 team_name、creator_name、assignee_name。两种批量请求都先去重，最多 100 个正 ID；前者只返回调用者仍活动的团队，后者要求调用者是该团队当前成员且只返回活动成员。

## 3. 任务详情

RPC：Task.GetTask；HTTP：GET /api/v1/teams/{team_id}/tasks/{task_id}

当前团队成员可读取。响应包含完整 TaskItem 与 can_update_status。can_update_status 仅在调用者是创建者、负责人或团队拥有者时为 true；它是界面提示，写入时 Task 仍重新核权。

不存在返回 NotFound；无团队资格返回 PermissionDenied，不返回任务正文。Gateway 使用与本人列表相同的有限补名规则。

## 4. 状态并发与创建

SetTaskStatusRequest.expected_status 是 proto3 optional 字段，字段号 4。F4 调用者必须显式提供 0、1 或 2；缺少 presence 返回 InvalidArgument。

Task 在现有 SELECT FOR UPDATE 事务内按以下顺序处理：
1. 重新核对写权限。
2. 当前状态已等于目标 status 时幂等成功。
3. 否则当前状态与 expected_status 不同，返回 Aborted，不写任务、操作、通知或 outbox。
4. 相同才执行既有事务写入。

HTTP PUT /api/v1/teams/{team_id}/tasks/{task_id}/status 的 JSON 同时要求 status 和 expected_status；Aborted 映射 409，任务页面显示“任务状态已变化，请重新确认”。

创建继续使用 POST /api/v1/teams/{team_id}/tasks 和 Idempotency-Key。Vue 第一次提交生成键并冻结规范化请求；结果不确定时先按详情核对，再以原键和原请求重试。状态超时同样先重读详情，再判断成功、可重试或冲突。

## 5. 来源消息上下文

RPC：IM.GetTeamGroupMessageContext  
HTTP：GET /api/v1/teams/{team_id}/groups/{group_id}/messages/{message_id}/context

请求只含正 team_id、group_id、message_id，不接受客户端调整窗口。IM 复用现有团队群当前成员、资格代际、关闭边界和消息归属检查，固定返回目标前最多 20 条、目标、目标后最多 20 条，共最多 41 条，按消息 ID 升序。TeamGroupMessage 保留 sender_type、initiator_id 与 mentioned_user_ids。

先通过群与团队授权，目标不存在返回 NotFound；无群资格返回 PermissionDenied。响应前再次复核资格，撤权或代际变化时不返回部分正文。Gateway 将所有 ID 和时间戳转为字符串。

## 6. 本人任务通知

RPC：Task.ListMyTaskNotifications

请求 team_id、cursor、limit 规则与本人任务相同。Task 只返回接收人为本人且团队资格仍活动的通知，并联结任务标题与当前状态。TaskNotificationItem 增加 team_id、task_title、current_status。

顺序为未读分区在前，各分区 notification_id 倒序。游标绑定筛选、首屏通知 ID 上界、当前分区与最后 ID。显式已读会使条目跨分区，因此成功已读后浏览器从空游标刷新，而不是继续旧游标。响应 unread_count 是当前筛选下的权威未读总数。

HTTP：GET /api/v1/task-notifications?team_id={id}&cursor={cursor}&limit={n}

Gateway 为当前页补 team_name 与 actor_name。逐条已读继续使用 PUT /api/v1/teams/{team_id}/task-notifications/{notification_id}/read；打开任务不自动已读。

## 7. 实时提示

WebSocket task_notification_changed 仍只携带 version、notification_id、team_id 字符串。消息页和任务页各自在当前路由生命周期内创建同一种客户端，离开路由时销毁，因此同一时刻只有一个连接。

共享响应式模块只保存“任务通知可能更新”的布尔信号。任务页收到提示或重连后串行刷新通知第一页；重复 notification_id 合并。成功刷新采用 Task unread_count，失败保留现有数据与提示圆点。退出或身份世代变化清除信号和任务私有状态。
