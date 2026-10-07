# 阶段7：退出完成后重新入队

2026-10-07。沿用户已确认的 A75 持久资格版本与 IM 永久关闭记录，以及 A16“离队退群、再入队需本人重新加入团队群”的规则，本步只改 User 的既有拥有者邀请入口。

## 本步实现与审查

1. `AddTeamMember`仍先验证登录拥有者与目标启用账号，再在 User 短事务锁目标 `team_members` 行。没有旧行时显式写普通成员、活动状态、版本 1；已活动返回重复；退出处理中返回 `FailedPrecondition`，不抢跑 IM 清理。
2. 旧行是`left`时，锁住成员后按相同 team/user/旧 generation 查询 User 拥有的 033 退出操作，只接受状态 1。缺失、仍待清理、非法版本或版本耗尽均拒绝；条件更新同一行的角色为普通成员、状态为活动、版本加一并刷新加入时间。旧退出操作保留，旧请求重试不指向新代际。更新失败整笔回滚。
3. Gateway 将 `FailedPrecondition` 的提示改成通用“暂时不能加入”，因为它现在也可能表示待清理，而不一定是用户被禁用。重入没有写 IM `group_members`；旧资格版本仍被 IM 永久关闭记录挡住，本人必须重新显式 Join 团队群。

验证：定向 User 测试覆盖首次加入、重复活动、待清理拒绝、已完成重入、缺失/待清理操作、版本耗尽及写入失败回滚；旧“非活动成员一律不能重入”测试收紧为“退出处理中不能重入”。`go test ./... -count=1 -timeout=120s`通过。sqlmock 只核对事务与 SQL 形状，不能证明真实 MySQL 并发锁、迁移或 IM 群关闭与新版本的双服务交错。031—033 尚未在真实 MySQL 执行，Compose 专用证书与云端部署仍待统一验收；A16/阶段7尚未完成。

本步修改：[User成员写入](../rpc/user/member.go)、[User成员测试](../rpc/user/member_test.go)、[旧资格测试调整](../rpc/user/membership_active_test.go)、[Gateway错误提示](../api/member.go)、[Gateway测试](../api/member_test.go)、[User说明](../rpc/user/README.md)、[架构决策](architecture-decisions.md)、[项目计划](project-plan.md)、[本文](stage7-team-rejoin-contract.md)。
