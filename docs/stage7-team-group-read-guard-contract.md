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
