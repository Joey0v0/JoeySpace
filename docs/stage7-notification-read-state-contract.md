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

PUT只请求所点ID，无正文。收到同ID及正合法时间成功后仅更新该项read_at，不改列表/游标/其他项；列表和标记结果时间都验证不早于created_at。网络/超时/无效响应保持原未读显示，错误明确“未能确认，请刷新或重试”，不宣称后端未写；同ID可显式安全重试。401/403清空并要求刷新，404保留原状态提示刷新，普通下一页故障保持旧游标。已读项不再次发请求。换Token/团队/成功登录reset使旧结果/错误/finally失效，包括A→B→A；不存在当前记录不发请求，不信任按钮闭包的旧范围。按钮捕获页面渲染世代，旧按钮不得作用到新范围相同ID。

缺read_at的新页面拒绝旧HTTP响应；RPC省略新增标量可表现0，但已读PUT的旧Task RPC不支持且拒绝，不伪造成功。最终部署先执行028（依赖027），再升级Task/Gateway，页面脚本no-store；只读旧页面可继续读旧字段但不支持新操作。真实迁移/浏览器验收仍待最终部署。

## 文件分工

| 执行项 | 绝对目录/分支 | 允许文件 |
| --- | --- | --- |
| Task已读查询与确认 | D:/zy/GoLang/go-im/.worktrees/assignee-backend / codex/stage7-notification-read-task | rpc/task/notification_list.go、notification_list_test.go，新notification_read.go、notification_read_test.go |
| Gateway已读转发 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway / codex/stage7-notification-read-gateway | api/task_notifications.go、task_notifications_test.go，新task_notification_read.go、task_notification_read_test.go |
| 页面显式确认 | D:/zy/GoLang/go-im/.worktrees/assignee-ui / codex/stage7-notification-read-page | examples/task-notifications.js、task-notifications.test.cjs、task-notifications-view.js、task-notifications-view.test.cjs |
| 主agent | D:/zy/GoLang/go-im / codex/stage7-notification-read-state | proto/生成、init/迁移、路由、共同文档、必要TCP组合验证/既有夹具兼容、集中测试和Git整合 |

子agent只允许编辑上述文件、格式与差异检查，不测试/build/提交/合main/push/部署。主agent统一保存共同可编译基线，三worktree同起点，无冲突整合后跑Node/Go定向及全仓回归。权限/数据库用替身的测试不写为真实MySQL/User运行验收。
