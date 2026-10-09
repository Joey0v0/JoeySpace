# F2 真实会话导航读契约

日期：2026-10-09。依据[已确认设计](frontend-f2-navigation-design.md)与[实施计划](superpowers/plans/2026-10-09-frontend-f2-navigation.md)。User、IM 拥有数据；Gateway 只以当前 Bearer 身份转发和组合显示字段。此文定义 F2 的新增读接口，不改变已有消息读写、入群和本人已读语义。

## 公共约定

- 新增 HTTP 接口沿用 `{code:0,msg:"success",data:{...}}`；所有 ID、最新消息 ID、游标和快照上界以正十进制**字符串**返回。请求游标也为十进制字符串；零/省略表示首页或末页，limit 默认 20、最多 100，非法输入 HTTP 400。
- 无 Bearer 或失效为 401；有效身份但无团队资格为 403；不存在或跨团队的群为 404；陌生私聊对象为 404；服务不可用/超时分别按现有 Gateway 映射为 503/504。不得把依赖失败转成空目录。每次 RPC 从 metadata 验证身份，不接受客户端指定本人 ID。
- `ListMyTeams`、群目录、私聊目录均为独立分页，不承诺跨类型全局时间序；`next_* = "0"` 表示无下一页。浏览器不得推断未读或已读。

## User

| RPC / HTTP | 请求 | 成功 `data` | 授权与来源 |
| --- | --- | --- | --- |
| `ListMyTeams` / `GET /api/v1/teams` | `after_team_id`, `limit` | `teams:[{team_id,name,role}]`, `next_after_team_id` | 仅登录本人、启用账号、`team_members.membership_state=active`；以 team ID 键集分页，团队资料从 User 查询。 |
| `BatchGetConversationDisplayNames` / 无公开 HTTP | `user_ids`，1—100 个去重正 ID | `users:[{user_id,display_name}]` | 仅供 Gateway 对 IM 已证实的本次私聊对象补名；昵称优先、用户名兜底。未知/停用者不返回条目，Gateway 中性兜底。不返回邮箱、状态或其他资料。 |

内部显示名 RPC 沿用现有服务间调用条件，不新增独立服务身份隔离；Gateway **必须**先经 IM 查到本人持久会话，再请求对应 peer 的名称，不提供按任意 ID 查询的 HTTP 入口。

## IM 群

- 既有 `ListTeamGroups` / `GET /api/v1/teams/:team_id/groups` 保持“当前团队成员可以发现该团队全部群”的语义。每个群追加 `joined:boolean`，仅当前 `group_members` 记录匹配活动团队资格、generation 和关闭保护时为 true。列表查询后如团队资格改变，要拒绝旧成功结果。
- `GetTeamGroup(team_id,group_id)` / `GET /api/v1/teams/:team_id/groups/:group_id` 返回 `group:{group_id,name,owner_id,joined}`。直接链接按 ID 查询，不扫描列表页；团队无权 403、群不存在/不属该团队 404。`joined=false` 只允许展示资料和本人显式加入入口，不能据此阅读群消息。
- 入群继续使用 `POST /api/v1/teams/:team_id/groups/:group_id/join`；F2 不自动调用，也不设主群。

## IM 私聊

- `ListMyDirectConversations` / `GET /api/v1/me/direct-conversations` 查询参数 `snapshot_upper_message_id`, `before_last_message_id`, `limit`。成功返回 `conversations:[{peer_id,display_name,last_message_id}]`, `snapshot_upper_message_id`, `next_before_last_message_id`。第一页两游标均为零，服务端固定当前最大持久消息 ID；后续必须携带同一上界及上一页的 `next_before_last_message_id`。
- 仅 `messages` 中 `chat_type=direct` 且本人是 from/to 的消息参与计算；每个 peer 在快照上界内取 `MAX(id)`，以该 ID 降序、`< before_last_message_id` 键集分页。`offline_messages` 不能创建会话。未要求双方仍在同一团队。服务器不能接受客户端指定本人 ID。
- `GetMyDirectConversation(peer_id)` / `GET /api/v1/me/direct-conversations/:peer_id` 返回 `conversation:{peer_id,display_name,last_message_id}`；仅本人和该 peer 有持久单聊消息时成功，未知 peer 为 404。单条查询中的显示名也必须基于 IM 成功结果。`peer_id` 必须为正且不能等于本人。
- `display_name` 是 Gateway 调 User 内部批量 RPC 后的显示字段；User 无条目时用“已停用成员”一类中性名称，不暴露不存在或停用的区别。IM 只返回 peer ID/最新持久消息 ID。

## 兼容与验证

新增 RPC 和 protobuf 字段使用新编号；旧 HTTP 路由字段与入群/历史/未读行为不改变。默认和上限沿既有目录规则。本契约不要求迁移；直接目录 SQL 先在隔离库 `EXPLAIN`，如需索引由主 agent 另记迁移、回滚和旧库兼容。验证包括双账号隔离、群可见但未入群、退出/重入代际、两方向私聊、快照翻页、大 ID、未知 peer 不查显示名及依赖故障不返回假空页。
