# 阶段7：通知逐条显式已读契约

日期2026-10-05，A68用户明确选择A。沿A66 Task数据归属、A67本人当前团队权限、A27原生页面。基线main `89e2a1e`，本批整合分支 `codex/stage7-notification-read-state`。本批最多八小步：讨论1、共同接口/迁移1、Task查询1/确认1、Gateway1、页面1、集成验证1/文档1。

## 存储与接口

现有 `task_status_notifications` 新增 `read_at TIMESTAMP NULL DEFAULT NULL`，初始化/028迁移一致；历史通知和新记录默认未读，首次本人确认用数据库 CURRENT_TIMESTAMP，重复确认不修改首次时间。不删除、不标回未读，不新增索引/通知服务或读取位置表。

- ListTaskNotifications的每项追加 `read_at_unix_ms`，0为未读，正数为首次确认UTC毫秒。Task SQL用 `COALESCE(CAST(UNIX_TIMESTAMP(n.read_at) * 1000 AS SIGNED), 0)` 输出；时间仍限安全整数范围0—253402300799999，已读时间不得早于通知时间。旧字段/游标不变，GET无写入。
- 新RPC `MarkTaskNotificationRead(team_id,notification_id)`，原单一Bearer经metadata。成功响应明确回显 `notification_id` 和正 `read_at_unix_ms`；参数均正int64，不接受recipient或时间。
- Gateway `PUT /api/v1/teams/{team_id}/task-notifications/{notification_id}/read`：无查询/正文，严格正十进制路径、单Bearer，转发原Token/两ID；响应 `data.notification_id` 为字符串，`data.read_at_unix_ms` 为正JSON整数。核对RPC回显ID/时间后输出；安全400/401/403/404/503/504/502映射。GET已有通知JSON追加必须存在的数字read_at_unix_ms，不省略0；不重复存is_read。

Task确认先用User检查当前团队得到本人，再做纯SQL短事务：按id/team/recipient取行锁，不存在一律NotFound不泄露他人记录；空read_at才更新数据库时间，重读固定首次时间并验证，失败回滚。事务不包含User网络调用。提交成功后重新核对当前团队/同一本人再返回；最终复核失败时拒绝响应，但已提交的已读可能保存，不能声称回滚，重入有资格后同记录重试读回。不声称跨服务原子授权。数据库提交不确定返回安全错误，同记录重试能收敛，无新增请求键。

## 页面操作与失败

控制器保持原scope/世代隔离，整页read_at合法才加载。每条显示“未读”或“已读”及首次确认时间，未读项有按钮。新增 `markRead(notificationID)`，只允许当前已加载/当前范围中的未读项；所有通知读取/写入串行互斥，state.loading在操作期间true，控件禁用，不让分页旧结果覆盖刚确认。不自动确认、不自动刷新其他页、不统计全库未读数。

PUT只请求所点ID，无正文。收到同ID及正合法时间成功后仅更新该项read_at，不改列表/游标/其他项；列表和标记结果时间都验证不早于created_at。网络/超时/无效响应保持原未读显示，错误明确“未能确认，请刷新或重试”，不宣称后端未写；同ID可显式安全重试。401/403清空并要求刷新，404保留原状态提示刷新，普通下一页故障保持旧游标。已读项不再次发请求。换Token/团队/成功登录reset使旧结果/错误/finally失效，包括A→B→A；不存在当前记录不发请求，不信任按钮闭包的旧范围。控制器追加只读epoch getter用于按钮捕获范围世代，click前syncScope再比较；旧按钮不得作用到新范围相同ID。

缺read_at的新页面拒绝旧HTTP响应；RPC省略新增标量可表现0，但已读PUT的旧Task RPC不支持且拒绝，不伪造成功。最终部署先执行028（依赖027），再升级Task/Gateway，页面脚本no-store；只读旧页面可继续读旧字段但不支持新操作。真实迁移/浏览器验收仍待最终部署。

## 文件分工

| 执行项 | 绝对目录/分支 | 允许文件 |
| --- | --- | --- |
| Task已读查询与确认 | D:/zy/GoLang/go-im/.worktrees/assignee-backend / codex/stage7-notification-read-task | rpc/task/notification_list.go、notification_list_test.go，新notification_read.go、notification_read_test.go |
| Gateway已读转发 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway / codex/stage7-notification-read-gateway | api/task_notifications.go、task_notifications_test.go，新task_notification_read.go、task_notification_read_test.go |
| 页面显式确认 | D:/zy/GoLang/go-im/.worktrees/assignee-ui / codex/stage7-notification-read-page | examples/task-notifications.js、task-notifications.test.cjs、task-notifications-view.js、task-notifications-view.test.cjs |
| 主agent | D:/zy/GoLang/go-im / codex/stage7-notification-read-state | proto/生成、init/迁移、路由、共同文档、必要TCP组合验证/既有夹具兼容、集中测试和Git整合 |

子agent只允许编辑上述文件、格式与差异检查，不测试/build/提交/合main/push/部署。主agent统一保存共同可编译基线，三worktree同起点，无冲突整合后跑Node/Go定向及全仓回归。权限/数据库用替身的测试不写为真实MySQL/User运行验收。

## 本批实现与审查

本批八个小步骤都围绕“本人逐条确认通知已读”，未改变通知接收规则或增加实时推送。

| 步骤 | 实际改动、目的与解决的问题 | 主要定位 |
| --- | --- | --- |
| 1. 确认已读含义 | 先讨论显式点击、页面自动确认和阅读位置，用户选择A；把刷新与本人确认分开 | architecture-decisions.md A68 |
| 2. 固定存储和协议 | Task通知表增加可空read_at，准备028/init，列表追加时间和新增确认RPC；三分支从共同提交90789f3开始 | 028、task.proto及生成文件 |
| 3. 查询已读状态 | 仍按本人/当前团队分页，返回首次已读时间，0为未读；校验时间，不让GET改变状态 | rpc/task/notification_list.go |
| 4. 保存本人确认 | 权限检查后短事务锁定自己的记录，只在未读时写数据库时间；提交后再复核资格，重复重试保留首次时间 | rpc/task/notification_read.go |
| 5. 接通HTTP | 路由接入PUT，拒绝正文、查询参数和自报身份，校验RPC回显；列表追加数字已读时间，ID继续用字符串 | api/main.go、task_notification_read.go、task_notifications.go |
| 6. 接通页面 | 显示未读/已读及首次时间，只给当前未读记录提供按钮；成功才改变该项，失败提示刷新或重试，换账号/团队隔离旧按钮和结果 | 两份通知JS及对应测试 |
| 7. 集中整合与验证 | 主agent审查、保存Task43ebe59/Gateway7af2ad1/页面ab9f113，再无冲突合入本批整合分支；补既有HTTP/TCP列表验证 | Go/Node测试与task_notifications_transport_test.go |
| 8. 更新部署与审查文档 | 标明028依赖027、先迁移再升级；记录幂等、提交后撤权的不确定边界及实际验证范围 | 本契约、三个README、方案/决策/协作文档 |

调用链：页面指定一条通知 → Gateway PUT → Task确认RPC → User核对本人当前团队 → Task短SQL事务锁记录/保存首次时间 → User再次复核 → Gateway校验回包 → 当前页面世代只更新该条。查询链只读取，不自动执行确认。

### 验证结果与边界

- Task列表/确认定向测试、Gateway列表/确认定向测试通过。新增Task确认11个测试函数、Gateway确认5个测试函数，覆盖首次写入、重复/响应丢失重试、提交不确定、范围不匹配、权限前后复核、回滚/取消，以及实际本机TCP gRPC。
- 通知页面定向55项通过，整合后全部页面259项Node测试通过。页面验证使用VM/DOM/HTTP替身，覆盖逐条确认、失败重试、分页互斥、大ID和换范围后的旧按钮/旧响应隔离。
- 整合后 `go test ./... -count=1` 全仓通过；既有Agent/IM/User/任务/消息测试共同回归通过。测试使用临时Go缓存和串行包构建，没有新增依赖。
- Gateway的HTTP→实际TCP gRPC组合使用Task服务替身；Task生产确认处理器/生成客户端的TCP组合使用SQL/User替身。两组分别验证接线，不是完整真实User/MySQL端到端验收。
- 028仅准备，未执行真实迁移。真实MySQL时间/并发行锁、浏览器、容器和云环境仍待统一验收；没有新模型调用、实时提醒、全量未读数、批量全部已读或标回未读。

### 全部实际修改文件（相对本批main基线）

共26个文件，其中生成代码2个、测试7个；数据库仅新增一个已读字段及对应迁移。点击下面路径可逐项审查。

| 文件 | 本批用途 |
| --- | --- |
| [api/README.md](D:/zy/GoLang/go-im/api/README.md) | GET/PUT、页面已读和部署边界 |
| [api/main.go](D:/zy/GoLang/go-im/api/main.go) | 注册逐条已读PUT路由 |
| [api/task_notification_read.go](D:/zy/GoLang/go-im/api/task_notification_read.go) | 确认HTTP转发与回包校验 |
| [api/task_notification_read_test.go](D:/zy/GoLang/go-im/api/task_notification_read_test.go) | 确认参数/错误/HTTP与TCP组合 |
| [api/task_notifications.go](D:/zy/GoLang/go-im/api/task_notifications.go) | 列表返回和验证已读时间 |
| [api/task_notifications_test.go](D:/zy/GoLang/go-im/api/task_notifications_test.go) | 列表已读字段兼容与非法结果 |
| [api/task_notifications_transport_test.go](D:/zy/GoLang/go-im/api/task_notifications_transport_test.go) | 既有HTTP/TCP列表覆盖已读/未读 |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md) | 028依赖和升级顺序 |
| [deploy/mysql/init.sql](D:/zy/GoLang/go-im/deploy/mysql/init.sql) | 新库通知read_at定义 |
| [deploy/mysql/migrations/028_task_notification_read.sql](D:/zy/GoLang/go-im/deploy/mysql/migrations/028_task_notification_read.sql) | 旧库增加可空read_at |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A68选择、备选和代价 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 当前本地成果和未验收部分 |
| [docs/stage7-notification-read-state-contract.md](D:/zy/GoLang/go-im/docs/stage7-notification-read-state-contract.md) | 本批接口/协作/审查依据 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三分支实际交付和整合记录 |
| [examples/task-notifications.js](D:/zy/GoLang/go-im/examples/task-notifications.js) | 逐条确认状态、失败和世代隔离 |
| [examples/task-notifications.test.cjs](D:/zy/GoLang/go-im/examples/task-notifications.test.cjs) | 控制器已读行为验证 |
| [examples/task-notifications-view.js](D:/zy/GoLang/go-im/examples/task-notifications-view.js) | 已读显示、按钮和旧范围检查 |
| [examples/task-notifications-view.test.cjs](D:/zy/GoLang/go-im/examples/task-notifications-view.test.cjs) | 页面点击/展示/隔离验证 |
| [rpc/task/README.md](D:/zy/GoLang/go-im/rpc/task/README.md) | Task确认规则与迁移依赖 |
| [rpc/task/notification_list.go](D:/zy/GoLang/go-im/rpc/task/notification_list.go) | SQL查询输出首次确认时间 |
| [rpc/task/notification_list_test.go](D:/zy/GoLang/go-im/rpc/task/notification_list_test.go) | 新列表列与时间校验 |
| [rpc/task/notification_read.go](D:/zy/GoLang/go-im/rpc/task/notification_read.go) | 本人确认短事务和资格复核 |
| [rpc/task/notification_read_test.go](D:/zy/GoLang/go-im/rpc/task/notification_read_test.go) | 确认/重试/撤权/回滚/TCP验证 |
| [rpc/task/task.proto](D:/zy/GoLang/go-im/rpc/task/task.proto) | 已读时间和确认RPC契约 |
| [rpc/task/pb/task.pb.go](D:/zy/GoLang/go-im/rpc/task/pb/task.pb.go) | 按proto重新生成消息代码 |
| [rpc/task/pb/task_grpc.pb.go](D:/zy/GoLang/go-im/rpc/task/pb/task_grpc.pb.go) | 按proto重新生成RPC代码 |
