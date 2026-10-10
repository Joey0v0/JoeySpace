# IM RPC：会话、消息与访问权限

负责团队群与私聊目录、授权消息读取、分页/未读/已读、任务来源核验，以及机器人发送、Agent 后台上下文、群资格关闭和结构化提及等受限内部入口。正式 Vue 通过 Gateway 调用，WS 发送与后台处理继续向 IM 核权。

[整体架构](../../docs/architecture.md) · [HTTP 入口](../../api/README.md) · [部署](../../deploy/README.md)

## 数据与写入链

IM 业务管理群、群成员、消息、离线投递、阅读凭据、普通提及、机器人受理和触发 Outbox 等数据。**聊天消息当前由 Push 消费 Kafka 后落库**；IM 不替代 Push 的整条消息写入链。普通提及经 Push → IM 校验目标，再由 Push 同事务保存消息与关系。

团队目录可见不等于已入群；团队群读写同时复核当前团队资格、群成员与资格关闭版本。读取历史不自动已读，离线 ACK 只确认投递；单聊和团队群都通过具体消息 ID 显式保存本人阅读状态。

## 协议

| 文件 | 范围 |
| --- | --- |
| [im.proto](im.proto) | 群创建/加入、目录、历史、未读/已读、来源、私聊与离线 |
| [bot.proto](bot.proto) | Agent 固定任务结果的机器人受理 |
| [trigger.proto](trigger.proto) | 后台持久原消息的受限上下文与成员解析 |
| [leave.proto](leave.proto) | User 固定退出操作对应的群资格关闭 |
| [mention.proto](mention.proto) | Push 普通提及目标与代际核验 |

专用入口分别鉴别 Agent、User 或 Push 服务身份，不将它们开放为浏览器 API。机器人受理成功不等于每位成员已收到或已读。

## 本地运行

从仓库根目录运行；先设置 `IM_MYSQL_DSN`、`IM_JWT_SECRET`、`USER_RPC_ADDR`，准备数据库并启动 User：

```sh
go run ./rpc/im -f rpc/im/etc/im.yaml
go test ./rpc/im ./internal/ws ./internal/push
```

默认监听 `127.0.0.1:9002`，Snowflake 节点默认 3，由 `IM_SNOWFLAKE_NODE_ID` 覆盖。User 地址本地为 `127.0.0.1:9001`，Compose 内为 `user-rpc:9001`；WS / Agent / Task 的普通 IM 地址分别配置为相应环境的 9002。

完整机器人、触发、退出与提及要启用对应覆盖和独立证书；普通 IM 9002 不包含这些专用服务。已有库须先核对最新迁移，跨会话未读包含 036 提及表的读取依赖。

## 进一步阅读

[前端聊天契约](../../docs/frontend-f3-api-contract.md)、[未读与提及](../../docs/frontend-f3-overview-contract.md)、[单聊已读](../../docs/stage7-direct-unread-contract.md)、[团队群资格](../../docs/stage7-team-group-read-guard-contract.md)、[机器人方案](../../docs/agent-group-reply-design.md)。当前真实验收见[F6 报告](../../docs/frontend-f6-review.md)。

## 开发过程记录

下面保留早期逐步实现与验证细节，描述当时范围；当前启动方式与能力以上方说明为准。

<details>
<summary>展开历史实现记录</summary>

# IM RPC：群成员查询

`CheckGroupMember(group_id)` 要求调用方把登录 Token 放入 gRPC `authorization: Bearer <Token>` metadata；IM 自行验签，从 Token 得到用户 ID，联查 `group_members` 和 `groups.team_id`。请求不能指定用户 ID。旧群只核对群成员资格；团队群还将原 Token 交给用户与团队 RPC 的 `CheckTeamMember(team_id)` 核对当前团队资格。非成员返回 `PermissionDenied`，凭证无效返回 `Unauthenticated`，数据库或团队 RPC 故障返回 `Unavailable`。团队群访问依赖先执行 `003_group_team_id.sql` 迁移。

`CreateTeamGroup(team_id, name)` 是内部 RPC，使用 Bearer Token 确定创建者，并调用用户与团队 RPC 的 `AuthorizeTeamGroupCreation`；只有团队拥有者可创建。API Gateway 要求客户端提供 `Idempotency-Key`，将其作为 gRPC `idempotency-key` metadata 转发。键限 1～64 位 ASCII 字母、数字、`-`、`_`、`.`；客户端对一次创建操作生成一个键，重试必须复用，另一次创建必须换键。授权通过后，IM 在一个事务中写入群及群主成员；数据库按 `(owner_id, request_key)` 保证唯一。相同用户、相同键、相同团队与修剪后的群名会返回原群 ID；同键不同内容返回 `AlreadyExists`（HTTP 409）。群名须为 1～64 个字符。已有数据库需先执行 `004_group_create_request_key.sql`，否则创建会失败。该方法需要独立的 `IM_SNOWFLAKE_NODE_ID`，默认 3，不可与旧 API 或用户 RPC 的节点号重复。

`ListTeamGroups(team_id, after_group_id, limit)` 接收 Bearer Token，先调用用户与团队 RPC 的 `CheckTeamMember`，再按群 ID 升序读取所属团队的群。默认每页 20 条，最多 100 条；有下一页时返回 `next_after_group_id`。API Gateway 提供 `GET /api/v1/teams/{team_id}/groups`，可带 `after_group_id` 和 `limit`。团队成员可看到群目录，但这不代表已经加入每个群；发送消息与读取历史仍需单独核对群成员资格。

`JoinTeamGroup(team_id, group_id)` 是本人自行加入团队群的内部 RPC，用户 ID 只从登录 Token 获取。IM 先通过用户与团队 RPC 核对当前团队资格，再确认群属于该团队，最后写入角色为普通成员的 `group_members` 记录；重复加入返回成功。Gateway 提供 `POST /api/v1/teams/{team_id}/groups/{group_id}/join`，只需 Bearer Token，不接受代他人加入。旧群或其他团队的群返回 404。尚无离队接口；未来引入离队时必须同时处理旧群成员记录与 Push 投递资格，规则见 [架构选型记录](../../docs/architecture-decisions.md)。

`ListTeamGroupMessages(team_id, group_id, before_message_id, limit)` 是团队群历史 RPC：先复用群成员与当前团队资格校验，再确认群属于路径中的团队，最后从 `messages` 按消息 ID 倒序读取。`before_message_id=0` 从最新消息开始；下一页传回非零 `next_before_message_id`，0 表示没有下一页。默认 20 条、最多 100 条。Gateway 提供 `GET /api/v1/teams/{team_id}/groups/{group_id}/messages`，可带 `before_message_id` 与 `limit`。已有库启用前须执行 `005_team_group_history_index.sql`；真实 MySQL 查询计划和端到端链路尚未验证。

`CheckTeamGroupAccess(team_id, group_id)` 供 Agent 读取草稿前核对原 Token 对指定团队群的当前访问权：检查群成员、团队成员及群的确切团队归属，只返回成功或错误，不读取消息。群历史读取复用这项校验。已通过本地 gRPC 与 SQL 替身测试，并接入 Agent 草稿读取；真实 MySQL 未验证。选型见[架构记录](../../docs/architecture-decisions.md) A38。

`CheckTeamGroupMessage(team_id, group_id, message_id)` 供任务服务建立来源引用：复用当前群成员与团队成员校验，再核对消息确实属于指定团队群；只返回是否可引用，不返回消息正文。旧群、其他团队或其他群的消息均不能作为来源。任务列表即使显示来源 ID，读取原消息仍须经过 IM 的群访问校验。

`ListOfflineMessages()` 从 Bearer Token 确定本人，只读联查其 `offline_messages` 和 `messages`，按消息 ID 升序返回；重复拉取会保留未确认消息，不在读取时删除。Gateway 已提供同路径 `GET /api/v1/message/offline`，响应保持旧接口的消息字段和字符串 ID。`AckOfflineMessages(message_ids)` 也从 Token 确定本人，仅删除本人对应的离线记录，单次须为 1～1000 个正数 ID；重复确认返回成功。Gateway 的 `POST /api/v1/message/offline/ack` 接收字符串 ID 数组。旧 Gin 的读取与确认入口仍保留；演示页现由 Gateway 提供，并使用同源相对路径调用这些接口。读取目前未分页，与旧接口一致；大量积压消息时需另行设计分页。仅通过本地 SQL 替身、gRPC 和 HTTP 测试，未连接真实 MySQL。

本机启动前设置 `IM_MYSQL_DSN`（连接已有 `go_im` 数据库）和 `IM_JWT_SECRET`（与旧登录使用同一密钥），然后执行：

```powershell
go run ./rpc/im -f rpc/im/etc/im.yaml
```

默认仅监听 `127.0.0.1:9002`。本机直接运行 WS 需设置 `IM_RPC_ADDR=127.0.0.1:9002`；本机团队群校验及创建还需启动用户 RPC 并设置 `USER_RPC_ADDR=127.0.0.1:9001`。Compose 中分别使用 `im-rpc:9002` 和 `user-rpc:9001`。未设置 `USER_RPC_ADDR` 时旧群仍按原规则校验，团队群校验及创建拒绝放行。可用 `go test ./rpc/im ./internal/ws` 验证本地 SQL 测试替身、Token 校验、gRPC 调用和 WS 接线。Compose 已有配置但尚未实际启动，也未连接真实 MySQL、Redis 或 Kafka 做全链路联调。旧群路径仍只验 Token 和群成员记录，不查询账户是否后来被禁用。

阶段7的IMLeave清理监听默认关闭。启用时须同时设置`IM_LEAVE_LISTEN_ON`（独立于普通IM、Bot和Trigger的端口）、`IM_LEAVE_TLS_CERT_FILE`、`IM_LEAVE_TLS_KEY_FILE`、`IM_LEAVE_TLS_CA_FILE`；仅接受证书DNS SAN为`user.go-im.internal`的User客户端，只注册`IMLeave.CloseTeamGroupMemberships`。缺配置、证书或端口冲突均拒绝启动，不退回明文。启用前须执行032；当前基础Compose尚未挂载该专用证书或设置上述变量，User也尚未调用，故默认部署不会执行退出清理。[本批审查与测试边界](../../docs/stage7-team-leave-im-runtime-contract.md#本批实现与审查)。

2026-10-02 按 A43 贯通发送者身份：`TeamGroupMessage` 追加字段 7 `sender_type`、8 `initiator_id`，`OfflineMessage` 追加字段 9、10；原编号和 RPC 方法保持兼容。类型 1 为用户，2 为机器人，旧消息未带类型时按用户处理。IM 历史和离线查询读取新增列，Gateway 将发起人 ID 编成字符串。启用更新后的查询前，已有库必须完成[013 迁移](../../deploy/mysql/migrations/013_im_bots_and_message_sender.sql)。本地 SQL 替身与真实本机 gRPC 验证机器人及大整数发起人 ID，完整 Go 回归通过；真实数据库未执行。尚无机器人发送 RPC、生产 TLS 监听或资料配置，不能把查询支持当作已可回帖。[本轮范围](../../docs/agent-group-reply-design.md#身份字段传输与展示实现记录2026-10-02)。

## 专用机器人发送入口（阶段 6，A44—A47）

后续本轮实现 `im.IMBot/PostTaskCreatedCard`，只注册在同一 IM 进程的可选双向 TLS 监听；原 `im.IM` 入口仍独立。请求携带正整数 `run_id`、`team_id`、`group_id` 和版本 1 卡片正文，原用户 Bearer Token 通过 metadata 提交，不提供调用方填写机器人或用户 ID 的字段。服务端先核对精确 Agent 证书身份，再核对 Token、当前团队群范围和配置的启用机器人。消息标识为 `bot-task:<run_id>`，首版一个运行只回传一项任务结果。

IM 在 [014 迁移](../../deploy/mysql/migrations/014_im_bot_sends.sql)的 `im_bot_sends` 保存固定正文、机器人/发起人/团队/群和事件时间，再同步写现有聊天 Kafka Topic。相同标识不同内容或范围返回 `AlreadyExists`；Kafka 失败或受理保存失败返回错误，不假报成功。再次调用会复核当前权限并重试同一事件，已经保存受理时不重新写 Kafka。MySQL 和 Kafka 非原子，并发相同调用或响应丢失可能重复投递，沿用 Push 消息去重及客户端 `msg_id` 去重，没有后台恢复。`accepted=true` 只表示已保存 Kafka 受理结果，不表示消息已存入群历史或送达所有成员。

机器人资料由 IM 表和部署配置选择，不自动创建或覆盖。IM 信任经过专用证书鉴别的 Agent 提交任务创建结果，不直接读取 Agent/Task 数据库，也没有在此新增 Task 查询；Agent 后续接线必须从持久化成功结果及冻结草稿构造卡片。卡片自身不授予 Task 访问权限。

默认所有 `IM_BOT_*` 变量为空，不启动回帖入口。启用须同时提供：

| 变量 | 用途 |
| --- | --- |
| `IM_BOT_LISTEN_ON` | 专用地址，例如本机 `127.0.0.1:9005` |
| `IM_BOT_CODE` | IM 表中已启用的机器人业务标识 |
| `IM_BOT_AGENT_DNS_NAME` | 精确的 Agent 客户端证书 DNS SAN |
| `IM_BOT_TLS_CERT_FILE` / `IM_BOT_TLS_KEY_FILE` / `IM_BOT_TLS_CA_FILE` | IM 服务端证书、私钥及信任 CA 的私有文件路径 |
| `IM_BOT_KAFKA_BROKERS` | 逗号分隔的 broker `host:port` |
| `IM_BOT_KAFKA_TOPIC` | 与当前 Push 消费 Topic 一致 |

依然需要现有 IM MySQL/JWT 与 User RPC 配置。缺失部分变量、无效证书或缺少 User RPC 依赖会在开启监听前失败，没有明文回退。请求处理最多 8 秒，Kafka 写入子步骤最多 3 秒、同步 RequireAll、最多一次内部尝试；未来 Agent/Gateway 接线须给整条创建＋回帖链另算预算。证书读取为启动快照，更新后重启 IM；未扩展其他 RPC 的加密。

普通 WS 不能发送内容类型 4，也不能提交 `bot-task:` 的大小写变体；新 WS 消息标识要求 1—64 个可见 ASCII 字符，页面 UUID 保持兼容，既有历史不更改。完整实际文件、测试及未验证范围见[本轮审查记录](../../docs/agent-group-reply-design.md#im-受保护发送入口实现记录2026-10-02)。

## 多项任务卡接收（2026-10-04，A55）

新 `IMBot.PostTaskCreatedCardItem` 仍只在上述专用 mTLS 监听提供，要求正 run/team/group、显式 `item_index`（0..4）和原 version-1 卡片；每次调用和已受理重放均核对精确 Agent 证书、原本人 Token、当前团队群权限及启用机器人。原 `PostTaskCreatedCard` 恒为第 0 项，两种入口共享该项记录。第 0 项仍 `bot-task:<run_id>`，其他项为 `bot-task:<run_id>:<item_index>`，同项同内容复用原时间/受理结果，同项换卡片或范围拒绝，不同项独立保存。

升级此版本 IM 前，已有库须完成 [020](../../deploy/mysql/migrations/020_im_bot_send_items.sql)，在原 IM 自有发送表追加默认 0 的项列；014 旧迁移不改，已有消息 ID、内容、时间和受理结果不改。旧 IM 不认识新方法，返回 Unimplemented；调用方不能回退旧入口发送非零项。先持久固定发送依据，再同步 Kafka，失败重试相同消息；accepted 仍不表示历史已落库或群成员送达。

本批只接收端和本机验证，Agent 集合确认尚不自动调用此方法，Agent 逐项回帖记录、独立重试 RPC/HTTP 和多项页面后续接入。[共同契约与修改范围](../../docs/multi-reply-im-contract.md)。真实 MySQL/迁移/Kafka、部署证书及容器未验收。

</details>
