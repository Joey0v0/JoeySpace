# 阶段7：User退出意图原子保存

2026-10-06，从417f3c9建立codex/stage7-team-leave-intent。本批沿A75/A77只增加User包内未公开的事务函数，不注册RPC/HTTP、不调用IM、不允许拥有者退出。目的：User撤权与033固定操作在同一事务提交或一起回滚，为后续受控IM清理和本人显式重试保留可靠起点。

## 共同函数与处理顺序

`beginTeamLeaveIntent(ctx context.Context, db *gorm.DB, teamID, actorID int64, requestKey string) (*teamLeaveIntent, error)`。actorID必须是未来公开入口先用Token验证的本人，函数自身不提供身份认证；本批绝不从请求自报ID开放RPC。`teamLeaveIntent`只返回固定operation ID/team/user/generation/status，不含资料或私有SQL。正team/actor，requestKey为1—64 ASCII字母数字、`-`或`_`，首尾不改写，大小写敏感。无效参数或取消在写入前拒绝。

User在本地短事务先锁定(team_id,actor_id)保留的team_members行，再按(user_id,request_key)查固定操作。已有同键且team/user/该代际一致时回显原操作，不再改变成员状态；同键不同team或代际报冲突。无操作时仅active、role普通/管理员、正generation允许开始；拥有者、leaving/left、坏数据、未知成员均拒绝。先在事务内把成员状态有条件改为leaving，再插入固定team/user/key/generation/status0操作；任一失败回滚两者。更新须检查影响行数正好1，不持锁跨RPC。两个不同键争同一成员时，行锁使后到者看到leaving后拒绝。返回只在提交成功后；提交失败/响应丢失均不得凭客户端猜测，后续同键查询/重试恢复。

实现时保留`team_members.generation`，退出时不递增；未来重新入队在IM清理完成后递增，旧清理关旧代际。不能把数据库唯一键当作Token权限证明。固定错误正文不暴露requestKey、SQL或用户资料；取消/超时返回相应状态，数据库故障Unavailable。MySQL8锁竞争与真实033另行验收。

## 分工

| 角色 | 绝对目录 | 分支 | 唯一允许文件 |
| --- | --- | --- | --- |
| A User事务函数 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-user-leave-intent | 仅新增rpc/user/team_leave_intent.go |
| B User事务测试 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-user-leave-intent-test | 仅新增rpc/user/team_leave_intent_test.go |

root拥有共同契约/文档、Git和全部测试；执行Agent仅在自己worktree/允许文件编辑、gofmt/diffcheck，不test/build/Git写/mainmerge/push/部署。两个worktree从同一共同提交建分支。测试使用sqlmock验证短事务顺序、回滚、同键重试、跨team复用、拥有者/状态拒绝、零版本/取消和错误保密；真实MySQL/进程崩溃不以替身冒充。
