# 阶段7：个人任务通知只读查询契约

日期：2026-10-05。沿 A65/A66/A67，仅提供当前团队成员读取自己持久任务通知的入口。本批不修改通知写入、不设置已读、不接实时推送或页面显示。

## 调用与响应

- Gateway：`GET /api/v1/teams/{team_id}/task-notifications?limit=20&before_notification_id=...`，Bearer Token 必须恰有一个；只允许 `limit`（1—100）和 `before_notification_id`（非负十进制）两个查询参数，省略时分别默认 20 和 0。HTTP 响应中的通知、任务、操作者及下一页 ID 均为十进制字符串。
- Task RPC：`ListTaskNotifications(team_id,before_notification_id,limit)`，authorization 经 metadata 传入；RPC 的 `limit=0` 使用默认 20，其他值为 1—100。输出 `notification_id, task_id, actor_id, from_status, to_status, created_at_unix_ms` 和 `next_before_notification_id`。时间是 UTC Unix 毫秒，不发送通知正文或团队成员资料。
- 按通知自增 ID 倒序，`before_notification_id=0` 查最新页，正数只查更小 ID；读取至多 `limit+1` 条，只有确实有下一页才返回最后一条已展示 ID，否则返回 0。空列表固定为 `[]`。

## 权限和失败

Task RPC 必须先用原 Token 核对请求人当前团队资格，得到本人 ID；SQL 同时限定 `n.team_id=请求团队`、`n.recipient_id=本人`、`n.id<游标`（非零时），并按操作 ID 关联 `task_operations` 取得不可变的行为人和状态变化。离队立即拒绝；重新入队后原记录重新可见。Gateway 只转发原 Token，不查询 Task 表、不允许客户端指定接收人，也不能把 RPC 私有错误内容回传浏览器。超时与不可用按现有 Task HTTP 错误风格映射。若结果形状与范围无效，Gateway 返回安全错误。

主 agent 统一维护 proto/生成代码、Gateway 路由、决策及共同文档；后端执行 worktree 只新增 `rpc/task/notification_list.go`、`notification_list_test.go`；Gateway 执行 worktree 只新增 `api/task_notifications.go`、`task_notifications_test.go`。各执行 agent 不修改协议、迁移、依赖、共同文档、main，不推送或部署。主 agent 统一审查、合入和验证。
