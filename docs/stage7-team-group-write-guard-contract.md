# 阶段7：团队群写入连接资格关闭保护

2026-10-06，起点ecf8bf8，集成分支codex/stage7-team-group-write-guard。沿用户确认A75/A76/A77，本批仅保护Join/Create写入并修复已有groups原始SQL引用，不开放退出/清理/重入RPC，不改读取权限模型、Push、页面、协议、迁移或依赖。整批六步：共同校验/契约、Join、Create、旧SQL引用、整合验证、审查记录。

## 固定接口与行为

- User核权在IM事务之前，继续转发原authorization。成功响应必须非nil、回显Token派生本人、generation正int64；缺失/不一致返回固定Unavailable，不把旧响应推测为版本1。
- root拥有team_group_authorization.go/_test.go共同校验。测试teamCheckFunc/teamCreateFunc默认42/1，仅为当前测试User替身，不是生产默认。
- Join先确认群属于请求团队，再withTeamGroupGeneration锁同team/user永久关闭行；generation<=closed拒绝且不写成员。新版本显式Join可写；重复成员INSERT的1062及存在核对在同一个受保护事务内完成，不能先释放关闭锁再当重复成功。组范围不存在仍NotFound。
- Create使用AuthorizeTeamGroupCreation的本人/版本，withTeamGroupGeneration替代原事务，群及群主成员同事务。重复建群仅在groups INSERT的1062时，于同一事务查固定owner/request_key；相同team/name返回原group ID且不补成员，不同内容AlreadyExists。群主INSERT失败回滚，不当作重复建群成功。原DB错误仅固定返回，事务/提交失败不得成功。
- 两个写入口不在SQL锁内调用User。关闭状态拒绝优先于任何重复写入成功；未开放清理RPC，测试以保存关闭记录模拟。
- 旧SQL仅正确引用groups表名和限定符，不改变查询参数/字段/权限/顺序。trigger原始多行常量需改为可包含反引号的Go字符串，最终SQL语义/空白保留。
- SQL替身验证事务/条件/回滚，不替代实际MySQL语法与锁竞争。031/032先于升级；新IM写入要求User返回正版本，须先升级User，不能混旧版本而默许。

## 三个执行任务

| 角色 | 绝对工作目录 | 分支 | 唯一允许文件 |
| --- | --- | --- | --- |
| A 自行入群 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-join-generation-guard | rpc/im/team_group_join.go、team_group_join_test.go；新rpc/im/team_group_join_generation_test.go |
| B 建群群主 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-create-generation-guard | rpc/im/team_group.go、team_group_test.go；新rpc/im/team_group_create_generation_test.go |
| C 旧SQL引用 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-group-sql-quoting | rpc/im/server.go、server_test.go、team_group_message_check.go、team_group_message_check_test.go、trigger_context.go；如确需新rpc/im/group_sql_quoting_test.go |

root统一共同文件、Git保存/整合与所有测试。执行Agent只编辑/gofmt/diffcheck，禁止test/build、Git写、越界、main合并/push/部署。不能修改team_group_fence.go；需共同修正报告root。共同提交由root保存后派发，三个worktree从同一快照建立干净分支。无需凑满九步。

## 当前验证边界

实现前记录。真实MySQL、迁移、并发锁、浏览器/容器/云/真实模型未验收。CheckGroupMember及后台读取的代际一致性尚未连接，不能把写入口保护等同完整退出；阶段7和A16保持未完成。
