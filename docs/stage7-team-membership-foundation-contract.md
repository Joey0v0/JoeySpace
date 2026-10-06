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
