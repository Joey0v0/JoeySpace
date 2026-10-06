# 阶段7：已确认退出方案的资格基础

2026-10-06，起点ad140b8，集成codex/stage7-team-membership-foundation。用户明确选择A75/A76/A77全部A。此批只建立活动状态/版本查询与IM关闭事务组件，不开放退出/状态/清理RPC，不启动后台或改Push。最多八步：共同协议/迁移、普通查询、后台查询、IM关闭组件、审查整合、集中验证、部署说明、记录。

## 固定契约

- User team_members追加membership_state：0 active、1 leaving、2 left；generation为正int64、默认1。031需先001，不能重跑。新库init补原先缺失的teams/team_members，并纳入031字段，旧库用增量迁移。
- CheckTeamMember仅活动成员，回显Token派生本人、角色、正generation；AuthorizeTeamGroupCreation仅活动拥有者，返回本人和正generation。无状态或无效版本不得被当成授权。普通目标/目录/角色/姓名候选/后台触发SQL都排除非active。新增字段兼容旧读取方，但后续版本保护必须协调升级、不能把缺版本猜成1。
- 普通CreateTeam/AddTeamMember继续靠数据库默认active/1写入。本批Add遇到现存离队状态仍拒绝重复，不提前开放重新激活；后续持久退出完成条件和递增版本接齐才开放重入。
- 032的im_team_group_fences拥有(team_id,user_id)主键及closed_through_generation默认0。该记录永久保留，不参与成员列表；消息/离线/已读凭据不删除。
- IM组件新文件team_group_fence.go：`withTeamGroupGeneration(ctx, db, teamID, userID, generation, write func(*gorm.DB) error) error`，本地短事务初始化/锁同pair关闭行，generation>关闭版本才执行write；nil/非法参数拒绝，不跨RPC持锁。
- `closeTeamGroupMemberships(ctx, db, teamID, userID, generation) (int64, error)`：同一短事务初始化/锁关闭行；generation<=已关闭返回原值，不重复删；更大generation推进关闭边界并删该team/user全部群资格，同事务失败全回滚。返回持久closedThrough>=请求，只有事务提交才成功；固定DB错误不输出内容/凭证。
- 组件只包内可用，无HTTP/RPC清理入口或生产调用。本批不宣称现有Join/Create/Check/后台/Push已获得关闭保护，后续统一连接版本检查和受控服务身份。

## 执行范围

| 任务 | 绝对工作目录 | 分支 | 唯一允许文件 |
| --- | --- | --- | --- |
| A User普通资格 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-user-active-membership | rpc/user/team_member_check.go/_test.go、team_group_auth.go/_test.go、member.go/_test.go、member_list.go/_test.go、member_role.go/_test.go、team_member_target.go/_test.go、member_resolve.go/_test.go；新rpc/user/membership_active_test.go |
| B User后台资格 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-trigger-active-membership | rpc/user/trigger_team.go、trigger_member.go、trigger_team_test.go、trigger_member_test.go、trigger_member_flow_test.go、trigger_tls_flow_test.go；新rpc/user/trigger_membership_active_test.go |
| C IM关闭组件 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-im-group-generation-fence | 新rpc/im/team_group_fence.go、team_group_fence_test.go |

root统一协议/生成代码/模型/迁移/init、共同文件、测试、Git整合。执行Agent不修改其他文件、不test/build/Git写/合main/push或部署，只编辑/gofmt/diffcheck。共享旧测试helper由A拥有，B可调用但不编辑；root审查A后将其提供B运行组合测试。无需凑满八步，不扩为整个离队功能。

真实MySQL/迁移/进程崩溃/浏览器/Compose/云/模型仍未验收；测试SQL替身只能验证事务顺序/精确条件，不能代替实际数据库并发锁。用户此前全部353项Node是旧页面验证，本批不改页面不重复。

## 本批实现与审查

共同383cf62；root审查/验证保存A67a5960（普通User15文件）、Be7a9aba（后台User6文件）、C2fdb86f（IM组件2文件），无冲突整合838549e。所有测试及Git保存/整合由root执行，子Agent仅编辑允许文件并gofmt/diffcheck，三个worktree干净保留。root最终协议路径修订及共同文档在集成分支；main仍89e2a1e，没有合main/push/部署。

| 小步 | 实际改动及验证 |
| --- | --- |
| 1 共同基础 | 确认A75/A76/A77全部A；031 User成员生命周期默认active/1、032 IM永久关闭表、共享常量/模型、User追加资格版本及拥有者本人/版本字段；init补缺失团队基础。只生成必要User pb，按旧proto路径重生成后grpc文件最终无差异，没有删除生成文件 |
| 2 普通User资格 | 7个普通权限/目录/角色/候选文件限定active，资格返回正版本且核实同本人/合法角色，拥有者建群授权复用同一核权；既有SQL测试同步，新6函数覆盖非活动caller/目标、非法数据、角色条件更新及重复Add不激活。两个实际TCP测试核对9007199254740993版本精确；root普通User全包通过 |
| 3 后台资格 | 两份生产SQL限定候选和发起人active，4份测试同步；已有实际生产mTLS监听覆盖非活动拒绝和查询前后撤权，无姓名结果泄漏。UserTrigger仍2字段/精确IM角色，不扩为Push授权；root后台定向通过 |
| 4 IM本地事务组件 | 包内初始化/锁关闭行，新代际可写，旧代际拒绝；关闭推进与目标团队/用户群成员删除同事务，旧重试不再删，错误/取消不返回成功或私有驱动文字。8个测试主题含参数、坏行、重放、回调与阶段失败回滚；root定向通过。未接现有生产入口 |
| 5 审查整合 | root修新DELETE里的groups保留字引用及测试；确认读取资格、添加及角色修改不自动重新激活保留行；三分支无冲突整合，不跨RPC持事务锁 |
| 6 集中验证 | `go test ./... -count=1 -timeout=90s`全仓通过；TEMP GOCACHE、GOFLAGS=-p=1。031/init默认及检查约束静态一致，032/init完整DDL字节一致。没有实际执行SQL/迁移，不重复未变页面的353项Node |
| 7 部署说明 | 031须先001，升级User先迁移；032仅关闭组件表，初始化/迁移不执行退出或删成员。新库init包含teams/team_members，已有卷不用重新init代替迁移 |
| 8 记录 | 更新决策确认、计划、协作、验收及全部文件定位；退出/恢复/入群/Push接线和真实环境仍保留未完成 |

本轮权限调用链：客户端原Token→现有Gateway/RPC→User本人验签/账户核对→team_members限定active→返回核实身份/正版本；任务候选、后台IM mTLS核权也排除非活动成员。没有User离队写入入口，所以这些测试模拟非活动查询结果，不假称运行了真实退出。

本轮IM组件调用链仅在测试：本地事务→初始化不重置关闭行→FOR UPDATE锁同一team/user→比较/推进版本→有条件执行回调或定范围删群资格→提交；较旧清理在锁后返回原关闭值，无DELETE。组件中的版本是内部输入，不能独立当调用者授权，未来受控RPC必须派生/验证固定退出范围。

审查依据：MySQL8官方将GROUPS列为保留字，新SQL已引用`groups`。[官方关键字说明](https://dev.mysql.com/doc/refman/8.0/en/keywords.html) 旧IM原始SQL仍有未引用JOIN/FROM，已列下一批修复；sqlmock不执行SQL语法，不能据全仓通过宣称旧库联调成功。

未验证：031/032真实执行、MySQL事务锁竞争/死锁/真实重启，User→IM清理/Push→User专用mTLS、当前IM Join/Create/读取关闭保护、退出/恢复/重入页面、浏览器/Compose/云和真实模型。没有新依赖或后台Outbox恢复，不标A16或阶段7完成。

下面列出本批相对ad140b8的全部实际修改文件；对应路径来自Git差异，不省略生成文件或旧测试。

全部实际修改共36份：

| 文件定位 | 内容 |
| --- | --- |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md:3) | 迁移先后与当前未开放的业务接线说明 |
| [deploy/mysql/init.sql](D:/zy/GoLang/go-im/deploy/mysql/init.sql:17) | 新库团队基础/生命周期默认值与IM关闭表 |
| [deploy/mysql/migrations/031_team_membership_lifecycle.sql](D:/zy/GoLang/go-im/deploy/mysql/migrations/031_team_membership_lifecycle.sql:1) | User成员状态/正版本增量迁移（未执行） |
| [deploy/mysql/migrations/032_im_team_group_fences.sql](D:/zy/GoLang/go-im/deploy/mysql/migrations/032_im_team_group_fences.sql:1) | IM永久关闭表增量迁移（未执行） |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md:422) | 方案确认、批次/全部文件/验收边界及下一步记录 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md:7) | 方案确认、批次/全部文件/验收边界及下一步记录 |
| [docs/stage7-acceptance.md](D:/zy/GoLang/go-im/docs/stage7-acceptance.md:17) | 方案确认、批次/全部文件/验收边界及下一步记录 |
| [docs/stage7-team-leave-design.md](D:/zy/GoLang/go-im/docs/stage7-team-leave-design.md:3) | 方案确认、批次/全部文件/验收边界及下一步记录 |
| [docs/stage7-team-membership-foundation-contract.md](D:/zy/GoLang/go-im/docs/stage7-team-membership-foundation-contract.md:1) | 方案确认、批次/全部文件/验收边界及下一步记录 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md:515) | 方案确认、批次/全部文件/验收边界及下一步记录 |
| [internal/model/team_membership.go](D:/zy/GoLang/go-im/internal/model/team_membership.go:1) | 状态常量与IM关闭模型 |
| [rpc/im/team_group_fence.go](D:/zy/GoLang/go-im/rpc/im/team_group_fence.go:1) | 未接线的IM本地写入保护/关闭清理事务组件 |
| [rpc/im/team_group_fence_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_fence_test.go:1) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/member.go](D:/zy/GoLang/go-im/rpc/user/member.go:8) | 仅活动成员查询/授权或条件更新，资格回显正版本 |
| [rpc/user/member_list.go](D:/zy/GoLang/go-im/rpc/user/member_list.go:7) | 仅活动成员查询/授权或条件更新，资格回显正版本 |
| [rpc/user/member_list_test.go](D:/zy/GoLang/go-im/rpc/user/member_list_test.go:19) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/member_resolve.go](D:/zy/GoLang/go-im/rpc/user/member_resolve.go:8) | 仅活动成员查询/授权或条件更新，资格回显正版本 |
| [rpc/user/member_resolve_test.go](D:/zy/GoLang/go-im/rpc/user/member_resolve_test.go:24) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/member_role.go](D:/zy/GoLang/go-im/rpc/user/member_role.go:7) | 仅活动成员查询/授权或条件更新，资格回显正版本 |
| [rpc/user/member_role_test.go](D:/zy/GoLang/go-im/rpc/user/member_role_test.go:19) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/member_test.go](D:/zy/GoLang/go-im/rpc/user/member_test.go:22) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/membership_active_test.go](D:/zy/GoLang/go-im/rpc/user/membership_active_test.go:1) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/pb/user.pb.go](D:/zy/GoLang/go-im/rpc/user/pb/user.pb.go:176) | 按原协议路径重新生成并保留 |
| [rpc/user/team_group_auth.go](D:/zy/GoLang/go-im/rpc/user/team_group_auth.go:4) | 仅活动成员查询/授权或条件更新，资格回显正版本 |
| [rpc/user/team_group_auth_test.go](D:/zy/GoLang/go-im/rpc/user/team_group_auth_test.go:10) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/team_member_check.go](D:/zy/GoLang/go-im/rpc/user/team_member_check.go:7) | 仅活动成员查询/授权或条件更新，资格回显正版本 |
| [rpc/user/team_member_check_test.go](D:/zy/GoLang/go-im/rpc/user/team_member_check_test.go:20) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/team_member_target.go](D:/zy/GoLang/go-im/rpc/user/team_member_target.go:7) | 仅活动成员查询/授权或条件更新，资格回显正版本 |
| [rpc/user/team_member_target_test.go](D:/zy/GoLang/go-im/rpc/user/team_member_target_test.go:20) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/trigger_member.go](D:/zy/GoLang/go-im/rpc/user/trigger_member.go:16) | 仅活动成员查询/授权或条件更新，资格回显正版本 |
| [rpc/user/trigger_member_flow_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_member_flow_test.go:7) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/trigger_member_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_member_test.go:38) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/trigger_team.go](D:/zy/GoLang/go-im/rpc/user/trigger_team.go:7) | 仅活动成员查询/授权或条件更新，资格回显正版本 |
| [rpc/user/trigger_team_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_team_test.go:16) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/trigger_tls_flow_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_tls_flow_test.go:13) | 活动资格/版本或关闭事务的契约与回归测试 |
| [rpc/user/user.proto](D:/zy/GoLang/go-im/rpc/user/user.proto:54) | 追加本人/资格版本字段，方法保持不变 |
