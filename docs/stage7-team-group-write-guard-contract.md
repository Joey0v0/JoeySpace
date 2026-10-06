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

## 本批实现与审查

共同提交9c37bd1；A 7600a17、C c6c6526、B 83a41a6由root逐个审查、保存并无冲突整合到codex/stage7-team-group-write-guard，main仍89e2a1e。三个worktree干净保留，未合main/push/部署。会话中断的一次定向测试不计结果；root续作后在集成目录重新运行。

| 小步 | 实际改动与目的 |
| --- | --- |
| 1 共同契约 | 固定A75的入群/建群User响应必须回显本人、正generation；缺失拒绝，不能把旧User当版本1。共同校验及测试、三目录精确允许文件先于执行Agent发出 |
| 2 自行入群 | User核权→确认群归团队→同team/user永久关闭行短事务→版本可写才插入群成员。1062重复回查在持锁事务内完成且校验原群ID，关闭版本先于重复成功拒绝 |
| 3 建群群主 | User仅活动拥有者授权回显本人/版本→同代际锁下创建groups及群主group_members。仅groups插入1062查旧请求键；群主插入错误回滚。相同请求重试不恢复旧群成员，已关闭旧代际先拒绝 |
| 4 旧SQL修复 | 普通群核权、来源消息核查和Agent触发上下文手写SQL把MySQL保留字`groups`的表名/限定符正确引用；不调整过滤条件/参数或权限 |
| 5 集成验证 | root整合三个独立分支并新增真实本机User gRPC替身→生产IM gRPC调用的双TCP测试，检查9007199254740993旧版拒绝、+1显式允许及原Token转发。`go test ./rpc/im -count=1 -timeout=90s`通过（5.345秒）；`go test ./... -count=1 -timeout=90s`全仓通过（包括IM 5.271秒）。A/B新增测试覆盖非本人/缺版本、关闭前后、大ID、重复/提交/数据库失败；SQL用sqlmock，User为替身。静态`git diff --check`通过。前端未变，不重复上批353项Node |
| 6 文档与部署边界 | 更新决策、计划、验收与部署顺序；必须先001/031升级User，再032升级IM，旧User缺版本时新版IM拒绝。退出/清理/重入、读取端代际保护与Push资格仍后续，阶段7/A16不标完成 |

生产Join调用链：本人Token→IM验签→原Token到User.CheckTeamMember→回显本人/正版本校验→IM查群team范围→同pair关闭行初始化/锁→比较版本→群成员INSERT或锁内重复核对→提交成功才返回。Create同样先调用User.AuthorizeTeamGroupCreation，再锁同pair并创建群/群主；不会在持SQL锁时请求User。旧清理版本关闭后迟到的旧写请求不能再插入；跨服务时间窗口与读侧仍需后续措施。

真实MySQL的`groups`语法、031/032迁移、锁等待/并发交错与重启、浏览器、Compose、云和真实模型均未验收；双TCP+SQL替身证明协议/处理器行为，不证明数据库锁实现。现有CheckGroupMember、Agent触发后台读取、Push及离线访问尚未接同一代际关闭核对；本批不能宣称团队退出完整或投递已安全撤权。

相对ecf8bf8的全部实际修改共20份，均为本地文件：

| 文件定位 | 本批作用 |
| --- | --- |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md:3) | User/IM升级及031/032迁移顺序、当前边界 |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md:438) | A75已选落地、备选与代价 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md:7) | 当前进度与后续步骤 |
| [docs/stage7-acceptance.md](D:/zy/GoLang/go-im/docs/stage7-acceptance.md:17) | 验收边界与未执行项 |
| [docs/stage7-team-group-write-guard-contract.md](D:/zy/GoLang/go-im/docs/stage7-team-group-write-guard-contract.md:1) | 共同契约、执行范围与审查 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md:521) | 三分支实际协作记录 |
| [rpc/im/server.go](D:/zy/GoLang/go-im/rpc/im/server.go:45) | 普通群核权SQL保留字修复 |
| [rpc/im/server_test.go](D:/zy/GoLang/go-im/rpc/im/server_test.go:26) | 普通群核权SQL预期及User正版本替身 |
| [rpc/im/team_group.go](D:/zy/GoLang/go-im/rpc/im/team_group.go:47) | 建群及群主事务代际保护 |
| [rpc/im/team_group_authorization.go](D:/zy/GoLang/go-im/rpc/im/team_group_authorization.go:10) | 共同核对User本人/正版本 |
| [rpc/im/team_group_authorization_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_authorization_test.go:9) | 无效身份/版本及大版本行为 |
| [rpc/im/team_group_create_generation_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_create_generation_test.go:76) | 建群关闭、幂等及失败回滚 |
| [rpc/im/team_group_generation_flow_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_generation_flow_test.go:69) | User/IM双TCP生产处理器组合 |
| [rpc/im/team_group_join.go](D:/zy/GoLang/go-im/rpc/im/team_group_join.go:38) | 自行入群代际保护与锁内重复核对 |
| [rpc/im/team_group_join_generation_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_join_generation_test.go:50) | 入群关闭、正版本、重复与失败测试 |
| [rpc/im/team_group_join_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_join_test.go:22) | 原TCP/重复测试更新 |
| [rpc/im/team_group_message_check.go](D:/zy/GoLang/go-im/rpc/im/team_group_message_check.go:27) | 来源消息SQL保留字修复 |
| [rpc/im/team_group_message_check_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_message_check_test.go:21) | 来源消息SQL固定预期 |
| [rpc/im/team_group_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_test.go:34) | 建群User正版本替身/事务预期 |
| [rpc/im/trigger_context.go](D:/zy/GoLang/go-im/rpc/im/trigger_context.go:25) | 后台触发SQL保留字修复 |
