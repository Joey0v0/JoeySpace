# 阶段7：普通团队群读取代际保护

2026-10-06，起点8d2e672，集成codex/stage7-team-group-read-guard。沿已确认A75继续普通本人读取链，本批不扩UserTrigger专用RPC、不改Agent后台协议/推送/退出/清理/重入。最多六步：共同查询与契约、普通CheckGroupMember接线、团队群调用方测试、机器人/离线调用方测试、整合验证、记录。

## 固定调用与失败规则

- 初次群成员查询仍只读取team_id以确认群属性，不把该旧快照当最终授权。非团队旧群（NULL team_id）保持原群成员判断，不调用User或关闭行。
- 团队群转发原Bearer给User.CheckTeamMember，成功响应必须为原Token本人、正generation；业务拒绝/取消/超时及Unavailable映射沿现有规则。无效成功响应固定Unavailable，不读取私有DB后放行。
- User核权成功后，单条IM SQL重新核对当前group_members、该群仍属同team及(team,user)的closed_through_generation。LEFT JOIN允许尚无关闭行的旧活跃成员按0处理；已有行必须非负，generation>closed才放行。无当前成员或已关闭为PermissionDenied；坏数据/SQL失败固定Unavailable。该查询只能依赖真实User回显的generation，不从旧成员快照猜版本。
- 此检查没有跨服务原子事务，User撤权后已在途的读取不能保证瞬时零窗口；重新入队前User必须确认IM清理成功，才能保证上一代旧群成员行已移除。后台专用UserTrigger当前仅返回actor/team不返回generation，因此另批统一协议/生成和后台路径，不能把普通入口修复等同后台完成。
- teamGroupReadFenceSQL和其测试helper由root拥有，执行Agent只按契约调用；测试helper构造固定独立预期，不从生产SQL常量复制。没有新迁移/依赖或架构选型。

## 三个执行任务

| 角色 | 绝对工作目录 | 分支 | 唯一允许文件 |
| --- | --- | --- | --- |
| A 普通授权 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-check-group-read-fence | rpc/im/server.go、server_test.go；可新增rpc/im/server_read_generation_test.go |
| B 团队群读取测试 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-team-read-callers | rpc/im/team_group_access_test.go、team_group_history_test.go、team_group_message_check_test.go、team_group_unread_test.go、team_group_unread_recovery_test.go |
| C 离线/机器人测试 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-offline-bot-read-callers | rpc/im/offline_access_test.go、offline_messages_test.go、legacy_offline_flow_test.go、bot_reply_test.go、bot_item_reply_test.go、bot_item_flow_test.go |

root统一共同SQL/helper与本契约、所有Git保存/整合/Go测试/文档。执行Agent只编辑/gofmt/diffcheck，不test/build/Git写/mainmerge/push/部署。若发现其他实际调用方测试或架构问题先报root，不扩大文件。三个worktree从同一共同基线新建干净分支。SQL/User为替身，真实MySQL锁/迁移/浏览器/云仍最终验收。

## 本批实现与审查

共同1654cb2，A d16531f、C bbe4c1c、B 9a1ab52由root保存并无冲突合入codex/stage7-team-group-read-guard。main仍89e2a1e；三个worktree干净保留。没有合main/push/部署/执行迁移。

| 小步 | 实际改动、目的与结果 |
| --- | --- |
| 1 共同查询 | 新增一条原始SQL：在同一查询快照中刷新group_members、群team归属及032关闭版本；缺关闭行按0，坏/负关闭行拒绝；独立固定预期测试含9007199254740993精确比较。不改表/协议/依赖 |
| 2 普通授权 | CheckGroupMember先获原群team范围；团队群转发原Bearer到User，核实回显本人/正generation，再执行共同新查询。旧团队资格已关闭、当前成员消失或群范围改变拒绝；非团队旧群只沿原群成员规则。User错误先于新SQL返回，不持IM SQL锁等待User。生产仅8行逻辑增量，A补实际IM TCP及失败边界 |
| 3 团队群调用方 | B更新历史、消息来源、本人未读及恢复测试的成功查询顺序；撤权/旧群不伪造新查询。旧测试Reader43现由局部User替身回显真实Token本人43/正版本，避免宽松共同stub掩盖误授权 |
| 4 离线/机器人调用方 | C按实际成功路径补离线和机器人普通入口的当前成员/关闭记录SQL预期；群去重、撤权过滤、旧群和ACK语义保持；其余三个允许文件只读核对后无需改动 |
| 5 集成验证 | `go test ./rpc/im -run 'TestTeamGroupReadFence|TestCheckGroupMember' -count=1 -timeout=90s`通过；整合后`go test ./rpc/im -count=1 -timeout=90s`通过（5.289秒），`go test ./... -count=1 -timeout=90s`全仓通过（IM 5.753秒，Agent 12.332秒）。root统一执行，子Agent未test/build。Node页面未改，不重复此前353项。`git diff --check`通过 |
| 6 边界记录 | 更新计划/ADR/部署/验收及协作，列明所有文件。后台Agent专用UserTrigger只回显actor/team，无generation，仍需下一批统一协议/生成与IM读取接线；当前不宣称后台已获得关闭保护或团队退出完成 |

调用链：客户端本人Token→IM原群成员查询得到team范围→User.CheckTeamMember核对活动资格并回显本人/正generation→IM一条SQL复核当前群成员、群team及关闭版本→才向上层历史/未读/来源/机器人/离线返回允许结果。原群成员查询结果仅用于确定该走哪条User核权路径；最终授权使用刷新结果。SQL/User均用替身；本机TCP测试确实经过生产IM处理器，但User仍为替身。闭合边界阻止后来的旧版本读取，已在途读取仍有跨服务时间窗口。

尚未验证031/032迁移、真实MySQL查询/锁竞争与旧库数据、真实退出→清理→重入、浏览器/Compose/云/真实模型。后台Agent专用触发读取未接版本与关闭检查，Push未接User专用核权；A16和阶段7仍未完成。相对8d2e672全部实际修改共19份：

| 文件定位 | 本批作用 |
| --- | --- |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md:6) | User/IM及031/032升级顺序、读取接线边界 |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md:440) | A75现有选择的读取落地、备选与代价 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md:7) | 当前进度、下一步和完成标记 |
| [docs/stage7-acceptance.md](D:/zy/GoLang/go-im/docs/stage7-acceptance.md:19) | 真实验收边界与剩余缺口 |
| [docs/stage7-team-group-read-guard-contract.md](D:/zy/GoLang/go-im/docs/stage7-team-group-read-guard-contract.md:1) | 共同契约、三任务及结果 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md:525) | 三个分支与root实际协作记录 |
| [rpc/im/bot_reply_test.go](D:/zy/GoLang/go-im/rpc/im/bot_reply_test.go:36) | 机器人普通入口复核预期 |
| [rpc/im/legacy_offline_flow_test.go](D:/zy/GoLang/go-im/rpc/im/legacy_offline_flow_test.go:167) | 旧HTTP→IM离线读成功/恢复预期 |
| [rpc/im/offline_access_test.go](D:/zy/GoLang/go-im/rpc/im/offline_access_test.go:54) | 当前离线团队群复核预期 |
| [rpc/im/server.go](D:/zy/GoLang/go-im/rpc/im/server.go:63) | 普通团队群读取User本人/版本及刷新结果接线 |
| [rpc/im/server_read_generation_test.go](D:/zy/GoLang/go-im/rpc/im/server_read_generation_test.go:36) | 无效回显、旧快照、闭合、取消及TCP边界 |
| [rpc/im/server_test.go](D:/zy/GoLang/go-im/rpc/im/server_test.go:153) | 旧普通授权测试预期及错误码 |
| [rpc/im/team_group_access_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_access_test.go:35) | 团队群访问测试的新核权顺序 |
| [rpc/im/team_group_history_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_history_test.go:40) | 历史分页核权测试预期 |
| [rpc/im/team_group_message_check_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_message_check_test.go:37) | 来源消息核权测试预期 |
| [rpc/im/team_group_read_fence.go](D:/zy/GoLang/go-im/rpc/im/team_group_read_fence.go:16) | 当前成员+关闭记录单语句复核 |
| [rpc/im/team_group_read_fence_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_read_fence_test.go:14) | 独立SQL预期及闭合/大版本边界 |
| [rpc/im/team_group_unread_recovery_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_unread_recovery_test.go:196) | 恢复测试对替身边界的说明 |
| [rpc/im/team_group_unread_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_unread_test.go:35) | 未读/已读核权预期及本人43替身 |
