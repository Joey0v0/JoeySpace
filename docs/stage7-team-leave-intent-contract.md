# 阶段7：User退出意图原子保存

2026-10-06，从417f3c9建立codex/stage7-team-leave-intent。本批沿A75/A77只增加User包内未公开的事务函数，不注册RPC/HTTP、不调用IM、不允许拥有者退出。目的：User撤权与033固定操作在同一事务提交或一起回滚，为后续受控IM清理和本人显式重试保留可靠起点。

## 共同函数与处理顺序

`beginTeamLeaveIntent(ctx context.Context, db *gorm.DB, teamID, actorID int64, requestKey string) (*teamLeaveIntent, error)`。actorID必须是未来公开入口先用Token验证的本人，函数自身不提供身份认证；本批绝不从请求自报ID开放RPC。`teamLeaveIntent`只返回固定operation ID/team/user/generation/status，不含资料或私有SQL。正team/actor，requestKey为1—64 ASCII字母数字、`-`或`_`，首尾不改写，大小写敏感。无效参数或取消在写入前拒绝。

User在本地短事务先锁定(team_id,actor_id)保留的team_members行，并核对teams.owner_id，真实拥有者即使成员role字段错误为普通/管理员也不能退出。再按(user_id,request_key)查固定操作。已有同键且team/user/该代际一致、操作状态与成员状态对应（待清理↔leaving，完成↔left）时回显原操作，不再改变成员状态；范围/代际或状态不符拒绝，不能伪成功。无操作时仅active、role普通/管理员、正generation允许开始；拥有者、leaving/left、坏数据、未知成员均拒绝。先在事务内把成员状态有条件改为leaving，再插入固定team/user/key/generation/status0操作；任一失败回滚两者，插入唯一冲突不能在同一事务里吞下后继续提交；两个不同团队并发共用同一用户请求键时，数据库1062唯一冲突固定映射AlreadyExists。更新须检查影响行数正好1，不持锁跨RPC。两个不同键争同一成员时，行锁使后到者看到leaving后拒绝。返回只在提交成功后；提交失败/响应丢失均不得凭客户端猜测，后续同键查询/重试恢复。

实现时保留`team_members.generation`，退出时不递增；未来重新入队在IM清理完成后递增，旧清理关旧代际。不能把数据库唯一键当作Token权限证明。固定错误正文不暴露requestKey、SQL或用户资料；取消/超时返回相应状态，数据库故障Unavailable。MySQL8锁竞争与真实033另行验收。

## 分工

| 角色 | 绝对目录 | 分支 | 唯一允许文件 |
| --- | --- | --- | --- |
| A User事务函数 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-user-leave-intent | 仅新增rpc/user/team_leave_intent.go |
| B User事务测试 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-user-leave-intent-test | 仅新增rpc/user/team_leave_intent_test.go |

root拥有共同契约/文档、Git和全部测试；执行Agent仅在自己worktree/允许文件编辑、gofmt/diffcheck，不test/build/Git写/mainmerge/push/部署。两个worktree从同一共同提交建分支。测试使用sqlmock验证短事务顺序、回滚、同键重试、跨team复用、拥有者/状态拒绝、零版本/取消和错误保密；真实MySQL/进程崩溃不以替身冒充。

## 本批实现与审查

1. root先固定未公开函数契约，目的为后续User退出入口保留同一事务的撤权与操作记录，不提前开放未经本人认证的RPC。共同契约为本文，未改协议或迁移。
2. A在`rpc/user/team_leave_intent.go`实现：锁定当前成员、核对`teams.owner_id`、识别同键重试，首次请求在User短事务内把active改为leaving并写033操作；失败回滚，数据库错误使用固定响应。解决“已撤权却无清理依据”及重复请求造成多次退出的问题。
3. B在`rpc/user/team_leave_intent_test.go`补事务顺序、同键重试、拥有者及无效状态拒绝、写入失败回滚和唯一键冲突测试。root修正GORM真实生成SQL中的`owner_id`引用匹配，移除排查时的临时错误透传；A和B的工作分别从`4550d78`、`1c15b6f`无冲突纳入当前分支。C只审计拥有者、状态一致性和回滚边界，没有改文件。

关键调用链目前仅为包内函数：未来经Token鉴别的User入口 → `beginTeamLeaveIntent` → User MySQL单事务。**没有公开RPC/HTTP，没有调用IM，也没有实际群资格清理。** 本机`go test ./rpc/user -count=1 -timeout=90s`通过；全仓结果以本批最终汇报为准。SQL仍为替身；真实MySQL 033、锁竞争、进程崩溃/重启和跨服务恢复均未验收。main未合并，也未push/部署。

本批全部实际修改文件：`rpc/user/team_leave_intent.go`、`rpc/user/team_leave_intent_test.go`、`docs/stage7-team-leave-intent-contract.md`、`docs/project-plan.md`、`docs/architecture-decisions.md`、`docs/stage7-acceptance.md`、`docs/worktree-collaboration-plan.md`、`deploy/README.md`。下一步才定义经Token认证的本人退出入口及显式重试接口，再分批接IM受控清理和Push核权。
