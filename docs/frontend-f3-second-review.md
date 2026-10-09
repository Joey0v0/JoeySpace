# F3 第二批 F10/F11 审查（2026-10-09）

基线 `a89ad4a`，集成分支 `codex/frontend-f3-chat`，用户已确认 F10 消息 ID 上界分页和 F11 保留 Push 写入链。本批九步均为本地实现与验证；未合并 main、推送、运行迁移或部署。

编号说明：总体前端路线是 **F0—F6 阶段**；这里的 **F09、F10、F11 是前端架构决定的流水编号**，分别对应 WS 票据、跨会话未读、普通成员提及，三项均归属 **F3 真实聊天阶段**。此前 F01—F08 是 F1/F2 讨论中确认的决定，不是“做到 F8 阶段”。

## 九步结果与调用链

1. **共同契约。** 固定 IM 本人未读 RPC、Gateway 字符串 ID、F11 结构化 ID 和 Push→IM 边界，提交 `fc3ae90`。解决前端按局部目录猜未读和 IM 写入归属误判。
2. **F10 IM。** `ec4e195` 加入跨群/私聊候选查询、本人鉴权、群与团队资格复核、已读计数和分页。`bc6d395` 补群名称及用户选定的 Snowflake 消息 ID 时间上界。调用链为 Gateway→IM→MySQL/User 资格；后页继续传上界与游标。小 ID 迟提交、已读或撤权可改变后页，不承诺严格固定行集。
3. **F10 Gateway。** `95b5fd1` 提供 `GET /api/v1/messages/unread-conversations`，Bearer 转发 IM，按当前页向 User 补私聊名称；ID 和计数以字符串返回。
4. **F10 Vue。** `5d14d6e` 接真实跨会话页、续页、刷新、重试及账号隔离；统一测试入口由 `bc6d395` 收录。点击会话仍由详情重新核权。
5. **F11 协议和资格。** `1d62939` 加 036 关系表、消息模型、专用 `IMMention` mTLS 监听与 Push 精确 SAN 校验，拒绝无权目标和不完整配置；`b05d134` 让 WS 校验最多十个字符串 ID 并带入 Kafka/实时帧，同 `msg_id` 指纹含 ID 集合。旧消息不反推关系。
6. **F11 Push 写入。** `8edc3fe`、`63d68f5` 保留 Kafka→Push→消息表链；有提及时由 Push→User 查当前团队代际、Push→IM 校验当前群成员，在同一事务写消息和关系。团队群普通用户消息即使提及集合为空也走关系感知重放；同 `msg_id` 增删或更换提及目标均拒绝，原有 `@AI` Outbox 同事务路径保留。`33cc747` 修正包含切片字段后的仓储测试比较。
7. **F11 IM 读取。** `3a8a986` 在经本人权限核验的历史和离线消息上批量加载关系 ID，并在 F10 查询中计数/筛选“@我”；Gateway 只把 ID 转成字符串，不依据正文判定。已读关系按既有显式已读记录减少。
8. **F11 Vue。** `a74456d` 让群聊从团队成员目录逐页选择最多十人，显示可移除标签；正文显示名称，WS 发送结构化 ID，重试保持原正文和 ID。服务端发送时核验实际群成员；历史/实时/离线按关系 ID 显示“提及了你”，总览 `@我` 使用 `mentions_only=1` 及权威提及未读数。`dd4c4f9` 配套新库 init、可选 Compose 覆盖及证书/迁移顺序。
9. **集中验证和审查。** 主 agent 核对各执行分支后整合，补 F10 群名称、上界、F11 空集合重放及前端跨筛选旧请求隔离；完成整仓 Go、前端构建和 59 项测试，并静态解析覆盖 YAML。结果和未验证项如下。

## 验证结果与限制

- `go test ./... -count=1` 全仓通过；IM、Gateway、WS、Push、仓储的定向测试也通过。关键替身用例覆盖资格拒绝、提及目标、相同消息 ID 关系冲突、`@AI` Outbox 事务、群历史/离线返回和 `@我` 已读计数。
- `npm run build` 含 `vue-tsc --noEmit` 通过，`npm test` **59/59** 通过。新增测试覆盖大整数提及 ID 原样传递、重复 ID 拒绝及切换“@我”时旧总览回包不能污染新筛选。
- `deploy/docker-compose.mentions.yaml` 通过 PyYAML 结构解析，`git diff --check` 通过。未在本机执行 036、真实 MySQL 查询/`EXPLAIN`、Docker Compose 启动、真实 mTLS 握手、Redis/Kafka、双账号群聊浏览器或生产反代。F10 聚合查询可能随消息量增长，真实执行计划必须在上线前核对；新 IM 代码引用 036 表，须先迁移再启动。
- 成员选择复用团队目录，可能列出尚未加入当前群的人；发送前 IM 会拒绝无资格目标。页面已明示这一点并支持目录续页。本批未做真实桌面浏览器视觉核对，输入法和物理键盘组合仍沿第一批限制。

## 全部实际修改文件

以下为 `a89ad4a..HEAD` 的全部文件（本审查文档及最终计划更新另列于末尾）：

- F10 API：[路由与处理](../api/frontend_f3_unread_conversations.go)、[测试](../api/frontend_f3_unread_conversations_test.go)、[注册](../api/main.go)；IM [协议](../rpc/im/im.proto)、[生成消息](../rpc/im/pb/im.pb.go)、[生成服务](../rpc/im/pb/im_grpc.pb.go)、[查询](../rpc/im/unread_conversations.go)、[测试](../rpc/im/unread_conversations_test.go)；前端 [状态](../frontend/src/messages/unreadOverview.ts)、[测试](../frontend/src/messages/unreadOverview.test.ts)、[页面](../frontend/src/messages/UnreadOverview.vue)、[测试脚本](../frontend/package.json)。
- F11 协议/模型/WS：[提及协议](../rpc/im/mention.proto)、[生成消息](../rpc/im/pb/mention.pb.go)、[生成服务](../rpc/im/pb/mention_grpc.pb.go)、[IM 启动](../rpc/im/main.go)、[IM 校验服务](../rpc/im/mention_service.go)、[服务测试](../rpc/im/mention_service_test.go)、[关系模型](../internal/model/message_mention.go)、[消息模型](../internal/model/message.go)、[事件](../internal/model/chat_event.go)、[WS 解析与输出](../internal/ws/protocol.go)、[WS 接收](../internal/ws/client.go)、[WS 测试](../internal/ws/client_test.go)。
- F11 Push/仓储：[Push 启动](../cmd/push/main.go)、[mTLS 客户端](../internal/push/mention_client.go)、[客户端测试](../internal/push/mention_test.go)、[写入与路由](../internal/push/pusher.go)、[仓储事务](../internal/repository/message_repo.go)、[关系重放测试](../internal/repository/message_mention_test.go)、[Agent Outbox 测试](../internal/repository/agent_trigger_repo_test.go)。
- F11 IM/API 读取：[提及批读](../rpc/im/mention_reads.go)、[群历史](../rpc/im/team_group_history.go)、[测试](../rpc/im/team_group_history_test.go)、[离线](../rpc/im/offline_messages.go)、[测试](../rpc/im/offline_messages_test.go)、[离线权限测试](../rpc/im/offline_access_test.go)、[旧离线链测试](../rpc/im/legacy_offline_flow_test.go)、[HTTP 群历史](../api/team_group_history.go)、[HTTP 离线](../api/offline_messages.go)。
- F11 前端：[会话容器](../frontend/src/messages/MessagesPage.vue)、[群聊视图](../frontend/src/messages/ConversationView.vue)、[历史模型](../frontend/src/messages/history.ts)、[离线模型](../frontend/src/messages/offline.ts)、[实时协议](../frontend/src/realtime/client.ts)、[实时测试](../frontend/src/realtime/client.test.ts)、[样式](../frontend/src/style.css)。
- 迁移与部署：[036](../deploy/mysql/migrations/036_im_group_message_mentions.sql)、[新库结构](../deploy/mysql/init.sql)、[覆盖配置](../deploy/docker-compose.mentions.yaml)、[环境模板](../deploy/.env.example)、[部署说明](../deploy/README.md)。
- 共同文档：[架构记录](architecture-decisions.md)、[第二批契约](frontend-f3-overview-contract.md)、[项目计划](project-plan.md)、[协作记录](worktree-collaboration-plan.md)、本审查文档。
