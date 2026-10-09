# F3 第二批共享契约（2026-10-09）

基线为 F3 第一批 `a89ad4a`。F10/F11 方向已获用户确认；本文件固定普通 RPC 与 HTTP 字段，功能完成情况及未验收边界见[第二批审查](frontend-f3-second-review.md)。

## F10 权威未读摘要

- `IM.ListMyUnreadConversations` 只从 Bearer metadata 确定本人，不接受 `user_id`。请求 `snapshot_upper_message_id`、`before_last_message_id` 为十进制正数或 0，`limit` 默认 20、最大 50，`mentions_only` 为布尔值。第一页两个游标均为 0；续页须原样传回上界和上一页的 `next_before_last_message_id`。
- IM 只返回本人目前有权读取的团队群、本人持久私聊；未加入或已离开的群不得出现在响应。每行有 `chat_type`、群的 `team_id/group_id/group_name` 或私聊的 `peer_id`、快照内 `last_message_id`、有限纯文本预览及时间、`unread_count`、`mention_unread_count`。群普通本人消息不计未读，机器人计入；私聊只计对方发给本人。阅读状态沿用已有逐消息记录，离线 ACK 不影响结果。
- 页面按 `last_message_id DESC` 跨类型分页。首屏记录当前时刻的 Snowflake 消息 ID 上界，不读取全库最大 ID；通常的新消息不进入旧页。用户选择此轻量语义：较小 ID 的迟提交消息理论上仍可能进入后页，不承诺跨请求固定行集或数据库事务快照。显式已读、离队、撤权仍会使后续页行消失，页面提供刷新从第一页重建。
- Gateway `GET /api/v1/messages/unread-conversations` 使用同名蛇形查询参数和 Bearer，转发 IM，按已有 User `BatchGetConversationDisplayNames` 仅为本页私聊补显示名。所有整数 ID 和计数以字符串返回，空列表是成功；IM/User 错误不伪装成“全部已读”。`mentions_only=1` 只用于结构化提及未读群。
- IM 查询不能在仅已加载目录上循环 N 次现有 `/unread` 冒充总览。真实 MySQL 执行计划和索引选择须在迁移前另审；本批先用已有消息/阅读索引和有限页，上线前实测查询计划。

## F11 普通成员提及

- 用户确认沿用既有 **Push 消息落库链**：Push 在写入前调用 IM 校验结构化提及目标，并将消息与关系写在同一数据库事务；IM 查询提及并在读取时复核本人群权限。相较最初“IM 写入”文字，这是按真实写入服务边界的实施修正，记录于架构决定。
- 只有团队群纯文本用户消息可带至多 10 个不重复的正成员 ID。浏览器 WS 帧 `data.mentioned_user_ids` 为十进制字符串数组；旧客户端省略表示无普通提及。与 `@AI` 的正文触发互不替代。WS/Kafka/持久化三段传递同一 ID 列表，`msg_id` 指纹包含列表，禁止同 ID 换提及对象。
- 新关系表以 `(message_id, mentioned_user_id)` 唯一，只保存经确认的新消息；旧历史为空，不从正文反推。消息表与关系同事务提交、同 `msg_id` 幂等重试核对相同关系。普通消息文字中的 `@名字` 本身不建立关系。
- 历史、实时与离线消息可返回 `mentioned_user_ids`；`@我` 总览只按本人未读关系及当前群资格计算。已读后从筛选消失；改名和重名不改变结构化 ID。页面从现有团队成员目录选择，正文显示名字，但发送的 ID 取选项实体。
- Push 到 IM 的内部校验须使用独立可信服务身份，不能把 Kafka 自报 `from_id` 当认证凭据，也不能把用户 JWT 长期写入事件。此受控接口、迁移和部署配置由主 agent 固定后实施。
