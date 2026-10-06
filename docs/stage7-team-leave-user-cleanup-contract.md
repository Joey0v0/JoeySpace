# 阶段7：User调用IM清理并确认退出

2026-10-06。沿用户已选择的A75同步清理＋本人显式重试方案，本步只接User包内流程，不开放普通RPC、Gateway或页面。

## 本步实现与审查

1. User新增可选`USER_LEAVE_IM_RPC_ADDR`及三项`USER_LEAVE_IM_TLS_*_FILE`配置。四项必须同时提供，启用时要求`-profile`；客户端固定核验IM证书名`im.go-im.internal`，使用专用mTLS、三秒调用超时，不转发用户Token。未配置时不建立连接，内部退出清理返回不可用。
2. `completeOwnTeamLeave`先用现有Token和启用账号验证本人，再按`(user_id, request_key)`查033固定操作。已有完成操作直接回显，包含后来重新入队的情况；待清理操作只允许同team和同代际`leaving`成员。首次请求由原`beginTeamLeaveIntent`事务原子撤权并保存固定操作。User释放数据库事务后才调用IM，必须收到`closed_through_generation >= 固定generation`。
3. IM确认后，User另开短事务，按“成员行→操作行”的统一顺序锁行，重验操作ID、请求键、team/user/代际和成员状态；条件更新成员为`left`及033为`completed`，使用UTC完成时间。任一更新失败整笔回滚。IM成功后User崩溃/回包不确定时，033仍待清理，本人沿同一请求键重试；IM重复清理依赖已有持久关闭版本幂等。
4. sqlmock测试覆盖固定范围、鉴别身份、远端失败不写User、已完成重放、代际变化拒绝、第二次更新失败回滚及客户端不转发Token/拒绝旧确认代际。`go test ./rpc/user -run 'TestCompleteOwnTeamLeave|TestFinishTeamLeaveIntent|TestTeamLeaveIMClient|TestBeginOwnTeamLeaveIntent' -count=1 -timeout=90s`和`go test ./... -count=1 -timeout=120s`通过。

限制：没有对外退出/恢复RPC，基础Compose没有证书和监听配置；031—033未在真实MySQL执行。本步没有真实User→IM双进程连通、MySQL锁竞争、回包丢失或浏览器验收，A16/阶段7仍未完成。Push当前资格、重入入口及公开本人操作继续后续小步。

本步实际修改：[User清理客户端](../rpc/user/team_leave_im_client.go)、[完成协调](../rpc/user/team_leave_complete.go)、[测试](../rpc/user/team_leave_complete_test.go)、[固定操作](../rpc/user/team_leave_intent.go)、[服务对象](../rpc/user/server.go)、[启动](../rpc/user/main.go)、[User说明](../rpc/user/README.md)、[本记录](stage7-team-leave-user-cleanup-contract.md)、[架构决策](architecture-decisions.md)、[计划](project-plan.md)。
