# 阶段7：个人任务通知只读查询契约

日期：2026-10-05。沿 A65/A66/A67，仅提供当前团队成员读取自己持久任务通知的入口。本批不修改通知写入、不设置已读、不接实时推送或页面显示。

## 调用与响应

- Gateway：`GET /api/v1/teams/{team_id}/task-notifications?limit=20&before_notification_id=...`，Bearer Token 必须恰有一个；只允许 `limit`（1—100）和 `before_notification_id`（非负十进制）两个查询参数，省略时分别默认 20 和 0。HTTP 响应中的通知、任务、操作者及下一页 ID 均为十进制字符串。
- Task RPC：`ListTaskNotifications(team_id,before_notification_id,limit)`，authorization 经 metadata 传入；RPC 的 `limit=0` 使用默认 20，其他值为 1—100。输出 `notification_id, task_id, actor_id, from_status, to_status, created_at_unix_ms` 和 `next_before_notification_id`。时间是 UTC Unix 毫秒，不发送通知正文或团队成员资料。
- 按通知自增 ID 倒序，`before_notification_id=0` 查最新页，正数只查更小 ID；读取至多 `limit+1` 条，只有确实有下一页才返回最后一条已展示 ID，否则返回 0。空列表固定为 `[]`。

## 权限和失败

Task RPC 必须先用原 Token 核对请求人当前团队资格，得到本人 ID；SQL 同时限定 `n.team_id=请求团队`、`n.recipient_id=本人`、`n.id<游标`（非零时），并按操作 ID 关联 `task_operations` 取得不可变的行为人和状态变化。成功查询在返回前再次复核当前团队资格和同一本人 ID，查询期间撤权则拒绝；代价是成功读取多一次 User RPC，这仍不是跨服务原子授权。离队立即拒绝；重新入队后原记录重新可见。Gateway 只转发原 Token，不查询 Task 表、不允许客户端指定接收人，也不能把 RPC 私有错误内容回传浏览器。超时与不可用按现有 Task HTTP 错误风格映射。若结果形状与范围无效，Gateway 返回安全错误。

主 agent 统一维护 proto/生成代码、Gateway 路由、决策及共同文档；后端执行 worktree 只新增 `rpc/task/notification_list.go`、`notification_list_test.go`；Gateway 执行 worktree 只新增 `api/task_notifications.go`、`task_notifications_test.go`。各执行 agent 不修改协议、迁移、依赖、共同文档、main，不推送或部署。主 agent 统一审查、合入和验证。

## 本批实现与审查

本批沿 A65/A66/A67，四个独立小步骤已完成本地验证：

1. **固定查询契约**：主 agent 增加只读 RPC/生成代码，明确团队、倒序游标和字符串 HTTP ID；解决 Task 与 Gateway 并行实现时接口不一致的问题。共同提交 `2ee6666`，使用仓库已有 protoc/插件，不增加依赖。
2. **Task 查询本人通知**：后端 agent 新增独立处理器，Token→User 当前团队资格→本人接收记录及操作信息→结果校验→User 二次资格复核。解决查看别人记录、查询期间撤权和分页边界问题；默认20/最大100，SQL限limit+1并校验隐藏的下一条。主 agent 审查与定向测试后保存 `e0e991d`。
3. **Gateway HTTP 转发**：Gateway agent 新增独立处理器，限定单一Bearer和两个分页参数，拒绝客户端自报接收人，转交原Token；校验RPC结果后把大ID编码为字符串，固定错误避免泄露内部详情。主 agent 增加生产路由，定向测试后保存执行分支 `2efaffd`。
4. **整合与回归**：主 agent 无冲突合入两分支，执行全仓Go测试，更新API/RPC/部署文档、A67决策、进度与协作记录；解决协议、路由和实现分散后遗漏接线的问题。整合分支 `codex/stage7-notification-read`，main仍 `8aa25a2`，没有推送或部署。

验证：Task定向 `go test ./rpc/task -run '^TestListTaskNotifications' -count=1`、Gateway定向 `go test ./api -run '^TestListTaskNotificationsHTTP' -count=1`、整合后 `go test ./... -count=1` 均通过。14个新增测试函数覆盖本人范围、撤权/身份变化、空/末页/大ID、异常参数与结果、超时/取消和安全错误；Task含生产处理器与生成客户端的实际本机TCP gRPC，SQL/User仍为替身，Gateway使用RPC替身。这不是实际HTTP→生产User→真实MySQL全链验收。本批未改页面，未重复Node测试；既有通知写入回归由全仓Go覆盖。

未验证：027迁移、真实MySQL时间转换与索引计划、跨服务并发离队/重入、真实容器/浏览器/云运行；二次资格核对仍无法提供跨服务原子权限快照。GET只读取，不表达已读或实时送达；页面入口待下一步。

本批相对main实际修改15个文件，含4个新增实现/测试、协议及两份生成文件、1个路由、7份文档；不包含上一批027/init/状态事务：

| 文件定位 | 实际变化 |
| --- | --- |
| [api/main.go](D:/zy/GoLang/go-im/api/main.go) | 注册通知GET路由 |
| [api/task_notifications.go](D:/zy/GoLang/go-im/api/task_notifications.go) | 新增HTTP参数/认证转发及响应校验 |
| [api/task_notifications_test.go](D:/zy/GoLang/go-im/api/task_notifications_test.go) | 新增5个HTTP测试函数 |
| [api/README.md](D:/zy/GoLang/go-im/api/README.md) | 通知读取及027依赖 |
| [rpc/task/task.proto](D:/zy/GoLang/go-im/rpc/task/task.proto) | 新增只读方法与消息 |
| [rpc/task/pb/task.pb.go](D:/zy/GoLang/go-im/rpc/task/pb/task.pb.go) | 重新生成消息代码 |
| [rpc/task/pb/task_grpc.pb.go](D:/zy/GoLang/go-im/rpc/task/pb/task_grpc.pb.go) | 重新生成RPC代码 |
| [rpc/task/notification_list.go](D:/zy/GoLang/go-im/rpc/task/notification_list.go) | 新增范围限定查询与资格复核 |
| [rpc/task/notification_list_test.go](D:/zy/GoLang/go-im/rpc/task/notification_list_test.go) | 新增9个后端测试函数及TCP组合 |
| [rpc/task/README.md](D:/zy/GoLang/go-im/rpc/task/README.md) | 说明只读查询与权限 |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md) | Task/Gateway共同升级和验收限制 |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A67选择及既定方案实施取舍 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 实际进度、分支及下一步 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 两个执行worktree范围及交付 |
| [docs/stage7-notification-read-contract.md](D:/zy/GoLang/go-im/docs/stage7-notification-read-contract.md) | 本批共同契约与全部审查依据 |
