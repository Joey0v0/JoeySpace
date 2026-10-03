# 阶段 6：任务创建结果群回帖方案（身份方向已确认）

日期：2026-10-02。用户已确认 A42 独立 AI 助手、A43 IM 资料、A44 双向 TLS、A45 结构化卡片、A46 同步回帖与本人重试，以及本轮 A47 IM 持久发送记录。存储、身份传输、卡片显示及 IM 的可选受保护发送入口已有本地验证；Agent 客户端/回帖执行、独立回帖记录和 HTTP/页面重试尚未接入，部署未启用。已实现的单项确认链见[验收清单](stage6-acceptance.md)。

## 业务问题和现状

用户确认草稿后，任务已能创建并在本人页面显示任务 ID；其他群成员尚不能在群内看到这次任务创建结果。下一段要把结果作为可查历史、可离线补拉的群消息发送，避免仅在当前页面显示一次。

现有普通消息链是 `浏览器 → WS → Kafka → Push → MySQL + 在线推送/离线记录`。IM 原先只有权限和历史查询，本轮新增独立机器人 RPC；普通 WS 发送边界保持原样。Push 每次查询当前群成员，普通用户消息跳过本人，机器人消息包含所有当前成员；已有 `msg_id` 唯一约束拒绝同 ID 不同内容，客户端处理至少一次投递的重复展示。WS 内部推送只负责在线投递，单独调用不会建立完整的历史和离线链路。

## A42：独立 AI 助手回帖（用户已定）

| 候选方案 | 群里看到什么 | 授权方式与改动 | 代价 |
| --- | --- | --- | --- |
| A：以发起人身份发送 | 消息发送者是本人，正文明确写“AI 辅助创建任务” | 原用户 Token 核对当前团队和群；后端固定结果和范围。复用普通用户发送者模型，仍需增加 IM 结果发送入口 | 改动较少，但不是独立机器人身份；现有 Push 跳过本人，本人须从确认结果或群历史看到该消息，后续正式机器人需再迁移 |
| B：独立机器人身份（推荐） | 消息发送者是 AI 助手，并能说明由谁确认创建 | IM 识别可信 Agent 调用并核对发起人的当前授权；机器人发送者由服务端确定，不由模型或页面填写。须设计机器人身份表示及调用凭证 | 产品语义符合微服务 + Agent 目标；增加身份、调用鉴别和消息显示改动。不能将机器人身份直接当作可访问所有群的权限 |

确认来源：用户在方案问答中选择“B：独立 AI 助手身份（推荐；需补身份与调用鉴别设计）”。采用 B 是为了把“谁生成结果”和“谁授权执行”明确区分：机器人负责展示，发起人授权本次操作。B 不意味着后台自动执行，也不要求增加语言、框架或消息中间件。具体身份存储和服务鉴别仍须讨论，不能先写一个特殊用户 ID 或伪造用户 Token 来代替设计。

选择 A 也能实现合规的本人操作链，但应记录为第一版身份取舍，不能把正文写有“AI”就当作机器人身份已经实现。

## A43：机器人资料归属与消息身份表示（用户已选 A）

当前 `users` 用于用户名、密码登录；`messages.from_id` 和历史、离线、实时协议均只表达普通用户。独立机器人需要真实的后端身份表示，不能仅在前端按某个 ID 显示“AI”，也不能把真人 ID 当作机器人 ID。

| 候选方案 | 设计 | 理由与代价 |
| --- | --- | --- |
| A：IM 独立机器人资料（推荐） | IM 持有机器人 ID、显示名与启用状态；消息增加发送者类型和发起人标识。用户消息的 `from_id` 指用户，机器人消息的 `from_id` 指机器人，均依据类型解释 | 用户登录不承担机器人管理；机器人没有用户密码或用户 JWT，不成为团队/群里的普通成员。代价是新增资料表、消息字段，并贯通 Kafka、Push、历史、离线和页面 |
| B：用户服务管理机器人账号 | `users` 增加账号类型，机器人复用用户 ID 空间；注册、登录和旧 API 均需禁止创建或登录机器人，成员权限也需识别账号类型 | 复用用户资料展示，但改变用户领域及所有认证入口；需要明确机器人是否入群、离队如何处理等生命周期，不能只插入一条有密码的普通用户 |

用户明确选择“A：IM 单独管理机器人资料（推荐；需扩展消息身份字段）”。IM 管理发言身份，User 管理真人账号，Agent 管理执行记录。首版仅需要一个受管理的 AI 助手资料，不提前新增机器人市场、创建页面或自动入群流程。机器人可以在哪个群发言，仍由本次发起人的当前资格及专用回帖入口限制；机器人资料存在不构成全群写权限。

按 A 推进，现存和普通 WS 消息须默认解释为用户发送，客户端不能通过上行参数伪造机器人类型；Push 只对用户发送者执行“跳过本人”，机器人回帖应投递给包括发起人在内的当前群成员。去重内容比较须包含新增身份字段，HTTP 与 WS 的 ID 继续使用字符串。字段、迁移和传输按独立小步骤实施，不在传输未贯通时启用机器人写入。

## A44：证明调用来自可信 Agent（用户已选 A）

服务身份与用户授权是两项独立检查：只有 Agent 服务凭证不能代替当前用户的群权限；只有用户 Token 也不能允许普通客户端冒充 AI 助手。

| 候选方案 | 设计 | 理由与代价 |
| --- | --- | --- |
| A：Agent→IM 双向 TLS（推荐） | IM 检查 Agent 客户端证书，Agent 检查 IM 服务端证书；专用回帖 RPC 另校验原用户 Token 和范围 | 复用 gRPC 标准能力，同时提供连接鉴别和传输加密，避免自行设计请求签名协议；代价是证书签发、挂载、有效期及更新配置 |
| B：专用密钥的 HMAC 请求签名 | Agent 对 RPC 方法、请求内容和用户凭证摘要签名；IM 校验签名、时间窗口和 nonce 防重放，再检查用户权限 | 无需第一步配置客户端证书；代价是自定义签名规范、密钥轮换、时钟与 nonce 存储。签名不提供传输加密，当前明文连接下 Token 和消息仍可被读取，跨主机部署仍需 TLS |

推荐 A 的依据是 gRPC 已提供 TLS 与客户端证书鉴别，不需要新框架或服务网格；见[gRPC 官方认证说明](https://grpc.io/docs/guides/auth/)。HMAC 是带密钥的消息认证机制，见[Go 标准库说明](https://golang.google.cn/pkg/crypto/hmac/)，不等同于加密连接。

用户明确选择“A：双向 TLS 证书鉴别（推荐；需配置与维护证书）”。拟在同一 IM 进程增加专用 TLS 监听，只注册机器人回帖服务；现有普通 IM RPC 地址和调用链保持兼容，不在旧明文入口注册机器人写入 RPC，也不把此次工作扩成所有服务的 TLS 迁移。专用监听仅在证书配置齐全时启用，配置缺失或无效不能退回明文。IM 除验证证书链外，还需明确只接受 Agent 的证书身份，不能把任意同一 CA 签发的客户端当作 Agent。用户 Token 仍通过该受保护连接提交，但不能承诺其他尚未迁移的服务连接已加密。

该监听由 IM 拥有并运行，不是新微服务；不新增机器人 HTTP 直通入口。部署证书和私钥不写入仓库；测试可在临时目录生成仅供本机验证的证书，不用于部署。双向 TLS 不代替业务幂等、任务结果核对、消息内容校验或投递回执。

## 回帖服务边界（IM 部分已实现，Agent 待接线）

- Agent 保存本次执行和回帖意图、固定内容及重试状态，调用 IM 发送结果；不直接写 IM 消息表或向 WS 内部推送接口投递。
- IM 拥有结果消息入口，核对团队、群与当前授权，验证允许的消息内容和发送者，处理稳定消息 ID/请求键的复用。预计继续经 Kafka 交给现有 Push 持久化与投递。
- Task 继续只拥有任务和任务创建去重；任务创建结果不等于群消息投递结果。

按 A44/A47 的用户确认，本轮只给 IM 增加专用写入入口和 Kafka 生产职责。普通 WS 发送路径保持原有边界，是否进一步收敛所有消息写入另行讨论，不夹带在本次结果回帖里。

## 已确认的结果投递规则

用户在 A46 明确选择本人显式操作：任务创建成功与回帖状态分开保存；回帖失败时保留已创建的任务 ID，用户重新核对当前权限后只重试回帖。消息 ID 和回帖正文冻结，重试不再次调用模型生成内容，不重新创建任务。备选后台自动回帖和恢复需要另定后台身份、权限有效期及调度；本次未选择，也不由 A41 隐含授权。

即使 IM 向 Kafka 成功写入，也只能报告“已受理”，不能报告所有成员已经收到；超时可能已受理，须保留原消息 ID 重试；Push 持久化、在线入队和浏览器实际收到是不同阶段。若产品需要确认“已保存群历史”或逐人送达，应另外增加结果查询/回执设计，不能只凭 Kafka 返回成功推断。

内容格式已在 A45 选择结构化任务卡片，协议定义与本轮验证见下方实现记录。普通文本摘要作为备选保留；客户端不能仅凭正文像 JSON 就将其当作任务卡片，也不能通过普通 WS 提交机器人卡片。

## 预计受影响范围与实施次序

用户已确认 A42—A46，依次实现存储、身份传输与鉴别前提、卡片协议/显示、受保护发送入口、回帖持久状态与本人重试。后续预计影响 `rpc/im/` 的机器人资料、发送契约与实现、`rpc/agent/` 的回帖记录和调用、`deploy/mysql/` 的必要迁移、`api/` 与 `examples/chat.html` 的状态和显示，以及 IM/Agent 启动、证书配置与 Compose 挂载。具体文件范围以每个实施小步骤的说明为准。

实施前的验收目标包括：当前权限失败不发送、不能伪造机器人、同键同内容返回同一消息标识、同键不同内容拒绝、任务成功后回帖失败不创建第二个任务、重启和响应丢失后本人可重试、成员在线与离线均可看到相同结果、至少一次投递按 `msg_id` 去重。以上都是拟议目标，当前没有运行验收或完成实现。

## IM 发送去重的补充选择（A47，用户已选 A）

实现受保护发送入口前发现：Kafka 受理与 Push 消息落库不同步，现有 Redis 去重只保存短期状态。缓存过期或丢失、历史尚未落库时，若仅复用 Redis，入口不能可靠拒绝相同消息标识下的新正文。

用户明确选择 A：IM MySQL 增加发送记录，先冻结消息标识、内容、时间和范围，再同步写 Kafka；成功后记录受理。它不是后台自动投递，不改变 A46 的本人重试规则。代价是新增表/014 迁移及数据库写入，Kafka 与 MySQL 非原子，超时后仍须重试同一消息。备选 B：复用现有短期 Redis，改动少，最终消息表仍去重，但长期重试和发送前冲突保护较弱。确认来源为用户在方案问答中选择“A：IM 持久发送记录＋同步 Kafka（推荐；需新增表和迁移）”；此后按 A 实施。

## 本轮已实现前提与审查文件（2026-10-02）

第一步实现存储身份：`im_bots` 定义 ID、业务标识、显示名与启用状态；`messages.sender_type` 为 1（用户）或 2（机器人），`initiator_id` 对机器人记录正整数授权用户 ID，对普通用户消息为 0。旧生产者不传类型时仍是用户。Repository 拒绝未知类型、缺少机器人/发起人 ID、机器人单聊及普通消息夹带机器人发起人，并将身份字段纳入重复 `msg_id` 的内容比较。这里是数据形状与去重校验，不是账号/群权限校验；没有读取机器人资料、配置机器人或发送消息的业务入口。

第二步实现独立的 TLS 凭证加载：客户端必须配置证书、私钥、CA 和 IM 名称；服务端要求可信客户端证书，并在标准证书链校验后检查精确的 Agent DNS SAN，不接受其他服务、通配符或仅相同 Common Name。最低使用 TLS 1.3，无明文回退；尚未接入生产启动，未来仍须在专用回帖 RPC 核对用户和群权限。

实际修改/新增文件如下，本清单仅指本轮改动，保留仓库此前未提交工作：

| 文件 | 本轮作用 |
| --- | --- |
| [internal/model/im_bot.go](../internal/model/im_bot.go) | 新增 IM 机器人资料模型 |
| [internal/model/message.go](../internal/model/message.go) | 增加发送者类型、发起人 ID 和类型常量 |
| [internal/repository/message_repo.go](../internal/repository/message_repo.go) | 普通消息兼容、身份形状验证及去重比较 |
| [internal/repository/message_repo_test.go](../internal/repository/message_repo_test.go) | 验证旧消息兼容、同 ID 身份冲突和非法身份不写库 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | 新库机器人表和消息字段 |
| [deploy/mysql/migrations/013_im_bots_and_message_sender.sql](../deploy/mysql/migrations/013_im_bots_and_message_sender.sql) | 已有库增量迁移，不插入机器人或凭证 |
| [internal/rpcauth/mtls.go](../internal/rpcauth/mtls.go) | Agent 客户端及 IM 服务端双向 TLS 凭证加载 |
| [internal/rpcauth/mtls_test.go](../internal/rpcauth/mtls_test.go) | 临时证书、真实本机 TCP gRPC 鉴别及配置错误测试 |
| [deploy/README.md](../deploy/README.md) | 013 的部署前提、TLS 接入边界和证书更新限制 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A43/A44 用户选择与本轮验证状态 |
| [docs/project-plan.md](project-plan.md) | 实际进度及下一小步 |
| [docs/agent-group-reply-design.md](agent-group-reply-design.md) | 更新已选方案、实现边界与完整文件清单 |

验证：`go test ./internal/repository -run 'CreateMessage|CreateOffline|DeleteOffline' -count=1` 通过；`go test ./internal/rpcauth -count=1` 通过；随后 `go test ./...` 全量通过。TLS 测试包含可信 Agent 成功，以及其他服务、通配符、错误客户端/服务端 CA、错误 IM 名称、过期 Agent 证书、错误用途、无证书及明文客户端被拒绝，失败不会到达测试 RPC 处理函数；缺文件、无效 CA、证书与私钥不匹配及空身份配置也被拒绝。

没有执行真实迁移、配置生产机器人、启用 TLS 监听、发送真实群消息或请求模型。本轮不改页面，未重复执行 Node 页面测试。Kafka/Push 的机器人身份传输、历史/离线协议、页面显示和机器人回帖投递尚待下一步贯通；不能先开启写入，再让其他环节丢弃身份字段。

## 身份字段传输与展示实现记录（2026-10-02）

这是后续一轮的三个小步骤，沿用用户已确认的 A43：

1. Kafka 与 Push 保留 `sender_type`、`initiator_id`，旧 Kafka 类型 0 按用户兼容。普通 WS 固定用户发送者，页面提交的机器人字段不能伪造身份。Push 的用户群消息继续跳过本人，机器人群消息投递给全部当前群成员，包括发起人以及与机器人 ID 数值相同的用户；在线载荷和离线关联保留同一消息身份。WS 下行将发起人 ID 转成精确字符串。
2. 历史与离线 protobuf 追加新字段，保留已有编号；IM 查询新增列，Gateway 以字符串输出发起人 ID。旧 IM 响应未带类型时按用户兼容；旧 Gin 离线响应也补齐字段。新增字段要求已有库完成 013，部署时应先更新读写链路与客户端，再启用未来机器人发送入口。
3. 原生页面实时、历史、离线及来源预览显示用户或 AI 助手与授权用户 ID；尚未查询机器人显示名。异常/未知元数据显示 `Unknown sender`，不猜测为机器人；消息正文仍按原有文字方式展示。继续按 `msg_id` 去重，已显示的离线重复记录仍可确认。不新增消息内容类型或任务卡片。

测试与实际范围：`go test ./internal/ws ./internal/push -count=1`、`go test ./rpc/im ./api -count=1`、`node --test examples/chat.test.cjs`（62 项）、`go test ./...` 均通过。Push 测试使用 JSON Kafka 事件、Repository/Redis/成员替身与本机 HTTP；IM 使用 SQL 替身和本机 TCP gRPC；页面使用 Node 模拟。没有运行真实 Kafka/Redis/MySQL、浏览器或容器，没有配置机器人资料或启用生产发送/TLS 入口，没有请求真实模型。

本轮全部实际修改文件如下；生成器沿用 `bin/rpc-tools` 已有固定版本，仅重新生成消息 Go 契约，未新增构建依赖：

| 文件 | 本轮作用 |
| --- | --- |
| [internal/ws/protocol.go](../internal/ws/protocol.go) | Kafka/浏览器消息新增身份字段，浏览器发起人 ID 用字符串 |
| [internal/ws/client.go](../internal/ws/client.go) | 用户发送链固定用户类型 |
| [internal/ws/client_test.go](../internal/ws/client_test.go) | 验证夹带机器人字段不能伪造身份 |
| [internal/ws/server_test.go](../internal/ws/server_test.go) | 内部推送保留机器人身份、兼容旧消息与大整数 |
| [internal/push/pusher.go](../internal/push/pusher.go) | 保存传输身份，机器人不跳过发起人或同 ID 用户 |
| [internal/push/pusher_test.go](../internal/push/pusher_test.go) | 验证用户兼容及机器人在线/离线全部成员投递 |
| [rpc/im/im.proto](../rpc/im/im.proto) | 两种历史/离线消息追加字段，不变更旧编号 |
| [rpc/im/pb/im.pb.go](../rpc/im/pb/im.pb.go) | 重新生成 Go 消息契约 |
| [rpc/im/team_group_history.go](../rpc/im/team_group_history.go) | 群历史读取并返回身份列 |
| [rpc/im/team_group_history_test.go](../rpc/im/team_group_history_test.go) | SQL 替身及本机 gRPC 保留用户/机器人身份 |
| [rpc/im/offline_messages.go](../rpc/im/offline_messages.go) | 离线只读拉取保留身份列 |
| [rpc/im/offline_messages_test.go](../rpc/im/offline_messages_test.go) | 重拉仍保持机器人及精确授权用户 ID |
| [api/team_group_history.go](../api/team_group_history.go) | 历史 HTTP 字符串身份编码及旧响应兼容 |
| [api/team_group_history_test.go](../api/team_group_history_test.go) | 同 ID 的用户/机器人编码验证 |
| [api/offline_messages.go](../api/offline_messages.go) | 离线 HTTP 字符串身份编码及旧响应兼容 |
| [api/offline_messages_test.go](../api/offline_messages_test.go) | 离线机器人和旧用户响应验证 |
| [internal/handler/message_handler.go](../internal/handler/message_handler.go) | 仍保留的旧 Gin 离线出口补齐字段 |
| [internal/handler/message_handler_test.go](../internal/handler/message_handler_test.go) | 旧出口身份与大整数兼容验证 |
| [examples/chat.html](../examples/chat.html) | 三路径及来源预览统一身份标签 |
| [examples/chat.test.cjs](../examples/chat.test.cjs) | 增加四项身份展示、合并与来源验证 |
| [api/README.md](../api/README.md) | 说明 HTTP/WS 字段、兼容与测试边界 |
| [rpc/im/README.md](../rpc/im/README.md) | 说明追加字段号、查询迁移前提及实际范围 |
| [docs/architecture-decisions.md](architecture-decisions.md) | 更新 A43 实现范围，新增 A45/A46 待选方案 |
| [docs/project-plan.md](project-plan.md) | 记录三个小步骤与下一处选型 |
| [docs/agent-group-reply-design.md](agent-group-reply-design.md) | 本轮审查记录及后续回帖选型材料 |

## A45/A46：下一段回帖的两项选择（用户已选 A）

身份链已准备好，但机器人还没有发送入口。2026-10-02 用户明确回复“两个选型我都选择 A”：确认结构化任务卡片，以及任务成功后同步尝试回帖、持久保存独立结果并由本人显式重试。以下保留推荐和备选理由；确认方案不等于完整功能已实现。

| 决策 | 推荐方案 | 备选与代价 |
| --- | --- | --- |
| A45 内容协议 | A：结构化任务卡片，带协议版本、精确任务 ID 和已确认标题摘要，由后端固定构造；发送者与授权用户仍用已有消息字段表达 | B：普通文本摘要，复用文本类型、改动少；但任务 ID/标题混在文字里，后续卡片还要迁移。A 符合目标产品的群任务卡片，代价是新增内容类型、校验及实时/历史/离线渲染和未知版本回退。卡片不能赋予 Task 权限，也不表示有新的任务详情接口 |
| A46 执行与恢复 | A：任务结果已持久保存后，同步尝试回帖；独立保存回帖内容、消息 ID 和受理状态，失败由本人重读后显式重试，仅重发原回帖 | B：持久后台回帖并自动恢复，需要另定后台授权、调度和并发；仅有服务证书不能代替离线用户授权。A 与 A41 的本人操作方式一致，代价是失败需要本人仍有权限并主动重试，不自动保证最终送达 |

推荐 A45 的结构化卡片，是为了让后续展示按明确协议扩展，避免让页面从自然语言里提取任务 ID；首版仅做任务创建结果，不自动扩展状态编辑或多项卡片。消息内容来自冻结草稿与实际 Task 结果，不再次请求模型生成回帖。

推荐 A46 的同步尝试与显式重试，是为了沿用当前身份链。必须将任务创建成功与回帖状态分开：回帖失败不撤销任务、不重建任务；响应丢失时仍复用相同消息 ID 和冻结内容。初次同步回帖是否有足够请求预算需要接线时验证，不能沿用 Task 超时预算就假定整个流程一定完成。

IM 向 Kafka 写入成功只能表示“已受理”，不能表示已保存历史或所有成员已收到；卡片文案与 API 状态应明确这个边界。若需要“已保存历史”或逐人送达确认，应另行设计消费结果查询/回执。A45/A46 已由用户确认；本轮完成卡片类型、校验与展示，发送 RPC、回帖状态表和执行链尚未实现。

## 任务卡片协议与展示实现记录（2026-10-02）

确认来源：用户回复“两个选型我都选择 A，接下来你继续进行下一步”。已将 A45/A46 的选择、备选、理由、代价与实施范围写入[架构记录](architecture-decisions.md)。这轮完成三个小步骤：

1. **定义内容协议**：`content_type=4` 表示任务创建结果卡片，正文为版本 1 的 JSON。只允许 `version`、`task_id`、`title` 三个字段；任务 ID 必须是正整数 int64 的规范十进制字符串，标题保持已确认的原值、非空且不超过 200 个 Unicode 字符。构造器按固定字段顺序编码相同结果，不调用模型；解码拒绝额外/错误字段、未知版本、数值 ID、越界 ID、空标题及损坏 JSON。构造器仅校验参数形状，不能单独证明任务存在或已获授权；后续 Agent 必须从持久化成功结果取任务 ID 与冻结标题。
2. **限制写入并验证透传**：Repository 只接受带机器人身份、发起人及群范围的合法卡片，然后沿用 `msg_id` 去重。同 ID 不同任务正文拒绝覆盖。现有 WS 上行只允许类型 1/2/3，新增测试证明夹带机器人身份的类型 4 在权限 RPC、Redis 和 Kafka 调用前即被拒绝。已有内容类型列、Kafka、protobuf、HTTP 和 WS 均可原样携带字符串正文，因此不新增迁移或字段；在 Push 在线/离线、IM 历史/离线 gRPC、Gateway HTTP 和 WS 下行原测试中补上卡片内容断言。
3. **统一接收展示**：页面实时、历史、离线共同使用卡片渲染函数，显示 `Task created`、已确认标题和精确任务 ID；来源预览显示可读摘要。标题使用 DOM `textContent`，不执行 HTML。身份异常、损坏内容或未知版本显示纯文字回退，继续处理后续消息；普通文本内的 JSON 不升级为卡片。跨接收路径仍按 `msg_id` 去重，离线重复及已显示的回退消息仍显式确认。卡片是创建结果快照，不显示实时状态、不新增任务详情调用或操作权限。

正文例子（外层消息继续携带机器人、发起人及群范围）：

```json
{"version":1,"task_id":"9007199254740993","title":"修复缓存问题"}
```

调用链：后续 Agent 根据持久化任务结果构造正文 → 受保护 IM 发送入口（待实现） → Kafka → Push 的 Repository 校验/入库 → 在线 WS 或离线记录；历史与离线经 IM RPC → Gateway；页面三路径按同一协议解码和显示。当前验证使用替身与本机 HTTP/gRPC，未启用真正机器人发送。

实际修改文件共 17 个，其中 10 个为测试文件；保留此前未提交修改：

| 文件 | 本轮作用 |
| --- | --- |
| [internal/model/task_card.go](../internal/model/task_card.go) | 类型 4、版本 1 结果协议与构造/解码校验 |
| [internal/model/task_card_test.go](../internal/model/task_card_test.go) | 精确 ID、稳定编码、Unicode 和非法协议验证 |
| [internal/model/message.go](../internal/model/message.go) | 内容类型注释说明卡片 |
| [internal/repository/message_repo.go](../internal/repository/message_repo.go) | 写库前限制机器人群卡片与内容校验 |
| [internal/repository/message_repo_test.go](../internal/repository/message_repo_test.go) | 非法卡片不入库、同消息 ID 结果冻结 |
| [internal/ws/client_test.go](../internal/ws/client_test.go) | 普通 WS 卡片伪造请求不触发副作用 |
| [internal/ws/server_test.go](../internal/ws/server_test.go) | 在线下行原样保留卡片和精确身份 |
| [internal/push/pusher_test.go](../internal/push/pusher_test.go) | Kafka 事件到在线载荷及离线关联保留同一结果 |
| [rpc/im/team_group_history_test.go](../rpc/im/team_group_history_test.go) | SQL 替身与本机 gRPC 群历史透传 |
| [rpc/im/offline_messages_test.go](../rpc/im/offline_messages_test.go) | 两次离线读取保持同一卡片与群范围 |
| [api/team_group_history_test.go](../api/team_group_history_test.go) | Gateway 历史 HTTP 保留正文与内容类型 |
| [api/offline_messages_test.go](../api/offline_messages_test.go) | Gateway 离线 HTTP 保留正文与内容类型 |
| [examples/chat.html](../examples/chat.html) | 共用卡片渲染、文字回退、来源预览 |
| [examples/chat.test.cjs](../examples/chat.test.cjs) | 新增六项卡片展示、异常回退与去重/确认验证 |
| [docs/architecture-decisions.md](architecture-decisions.md) | 记录 A45/A46 确认和本轮实施边界 |
| [docs/project-plan.md](project-plan.md) | 三个实际小步骤及下一步 |
| [docs/agent-group-reply-design.md](agent-group-reply-design.md) | 更新协议、当前边界与完整审查清单 |

验证：`go test ./internal/model ./internal/repository ./internal/ws ./internal/push ./rpc/im -count=1`、`node --test examples/chat.test.cjs`（68/68）及 `go test ./...` 全量通过。透传使用 SQL/业务替身、本机 HTTP/gRPC，页面使用 Node 模拟；未做整个生产链路联调。真实 MySQL/Kafka/Redis、浏览器、容器及模型未验证；尚无机器人资料配置、发送 RPC、生产 TLS 接线、Agent 回帖持久记录和显式重试入口。部署时先完成此前 013 迁移并更新完整内容/身份读写链路及客户端，再启用后续发送入口。卡片自身不需要新的 SQL 迁移。现有消费者对处理错误会保留偏移量并重试；后续 IM 入口必须在写 Kafka 前拒绝非法卡片，本轮未改变消费者或新增死信队列。

## IM 受保护发送入口实现记录（2026-10-02）

本轮四个小步骤，继续阶段 6：

1. **确定发言身份**：按部署的机器人 code 读取 IM 自有资料，每次发送都检查启用状态及合法 ID。没有用户密码、机器人 JWT 或自动入群；不自动创建或覆盖既有机器人。
2. **冻结发送依据**：在 A47 讨论并由用户明确选 A 后，新增 IM 拥有的 `im_bot_sends` 和 014 迁移。单项运行的消息标识固定为 `bot-task:<run_id>`，第一次单行 INSERT 提交后才能写 Kafka；以后正文、机器人、发起人、团队、群都必须一致，事件时间复用存储值。冲突返回 AlreadyExists；没有数据库事务跨 Kafka、没有自动后台投递。
3. **实现受保护发送处理**：专用 `IMBot.PostTaskCreatedCard` 不接受用户/机器人 ID，检查实际 TLS peer、原 Token、当前团队群归属、启用机器人与版本 1 正文，然后调用发送器。Kafka 事件 DTO 移到 shared model，旧 WS 类型保留别名，避免 IM 依赖 WS 网关代码。同步 Kafka 写入最多 3 秒、RequireAll、内部最多一次尝试；成功后持久保存受理，再返回 `accepted=true`。失败保留原记录，响应不确定可以重试原事件；并发相同请求可能重复发布，不承诺 exactly-once。
4. **进程与配置接线**：同一 IM 进程的可选 TLS 监听只注册机器人服务；普通入口只注册原 IM。完整参数与有效凭证才允许启动，缺少 User RPC 也拒绝启用。处理最多 8 秒；未来整个确认/回帖请求须另算外层预算。新 WS 消息 ID 限为 1—64 个可见 ASCII，拒绝保留前缀的大小写形式，防止现有 `utf8mb4_unicode_ci` 排序规则下普通客户端占用机器人结果；页面 UUID 兼容，旧历史保留。Compose 用独立覆盖文件显式启用，证书只读挂入 IM，不发布 9005 到宿主机。

调用链：

```text
可信 Agent（客户端待接线）
  → 专用 mTLS IMBot RPC
  → 原 Token + 当前团队/群范围 + IM 机器人资料
  → IM MySQL 固定发送记录
  → Kafka 同步受理
  → IM 保存 accepted（不等于群历史已保存或成员已收到）
  → 现有 Push → 消息表 + 在线 WS/离线记录 → 原卡片显示
```

IM 信任指定证书鉴别的 Agent 提交冻结的实际任务结果，未新增 IM→Task 查询，也不直接读 Agent/Task 表。Agent 后续必须从持久成功状态构造该请求。此处准备的是 IM 发送能力；Agent 回帖意图/执行、客户端证书配置、HTTP/页面显式重试还没有接入，基础 Compose 默认关闭入口。

全部实际源代码、生成契约、测试、配置和文档共 28 个文件，其中 6 个是测试文件、2 个是生成文件；此前未提交工作保留：

| 文件 | 本轮作用 |
| --- | --- |
| [rpc/im/bot_profile.go](../rpc/im/bot_profile.go) | 按配置读取启用机器人，隐藏数据库错误 |
| [rpc/im/bot_profile_test.go](../rpc/im/bot_profile_test.go) | 资料缺失、停用、非法身份及错误测试 |
| [rpc/im/bot.proto](../rpc/im/bot.proto) | 独立 IMBot RPC，不接入普通服务接口 |
| [rpc/im/pb/bot.pb.go](../rpc/im/pb/bot.pb.go) | 生成请求/响应 Go 契约 |
| [rpc/im/pb/bot_grpc.pb.go](../rpc/im/pb/bot_grpc.pb.go) | 生成独立服务和客户端契约 |
| [rpc/im/bot_send_store.go](../rpc/im/bot_send_store.go) | 持久冻结发送内容、时间、范围及受理状态 |
| [rpc/im/bot_send_store_test.go](../rpc/im/bot_send_store_test.go) | SQL 替身验证首次保存、冲突与重复受理 |
| [internal/model/task_card.go](../internal/model/task_card.go) | 共享机器人消息标识保留前缀 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | 新库增加 IM 发送表 |
| [deploy/mysql/migrations/014_im_bot_sends.sql](../deploy/mysql/migrations/014_im_bot_sends.sql) | 已有库发送记录增量迁移 |
| [internal/model/chat_event.go](../internal/model/chat_event.go) | 共享 Kafka 聊天事件 DTO |
| [internal/ws/protocol.go](../internal/ws/protocol.go) | 旧 Kafka 类型保留别名，格式不变 |
| [rpc/im/bot_publisher.go](../rpc/im/bot_publisher.go) | 先保存，再同步 Kafka，最后保存受理 |
| [rpc/im/bot_publisher_test.go](../rpc/im/bot_publisher_test.go) | 不确定结果重试同一事件，受理后不再发布 |
| [rpc/im/bot_reply.go](../rpc/im/bot_reply.go) | TLS 身份、原 Token、当前群权限及机器人校验 |
| [rpc/im/bot_reply_test.go](../rpc/im/bot_reply_test.go) | 身份、范围、离队、卡片及发布失败测试 |
| [rpc/im/bot_listener.go](../rpc/im/bot_listener.go) | 可选专用 TLS 监听、配置与生产构造 |
| [rpc/im/bot_listener_test.go](../rpc/im/bot_listener_test.go) | 临时证书真实 TCP gRPC、端口隔离及 YAML 静态验证 |
| [rpc/im/main.go](../rpc/im/main.go) | IM 进程接入可选监听和资源释放 |
| [internal/ws/client.go](../internal/ws/client.go) | 拒绝保留前缀、非可见 ASCII 与大小写变体 |
| [internal/ws/client_test.go](../internal/ws/client_test.go) | 保留消息标识绕过请求不触发副作用 |
| [deploy/docker-compose.bot.yaml](../deploy/docker-compose.bot.yaml) | 显式 TLS 覆盖、只读证书挂载，无宿主机端口 |
| [deploy/.env.example](../deploy/.env.example) | 新增非密钥的可选 IM 配置模板 |
| [deploy/README.md](../deploy/README.md) | 迁移、资料、证书和可选覆盖启用说明 |
| [rpc/im/README.md](../rpc/im/README.md) | RPC 契约、状态、预算与边界 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A47 用户确认及 A43/A44 当前状态 |
| [docs/project-plan.md](project-plan.md) | 本轮四个步骤、实际验证及下一步 |
| [docs/agent-group-reply-design.md](agent-group-reply-design.md) | 协议、完整审查清单与验证边界 |

验证：`go test ./rpc/im ./internal/ws ./internal/push -count=1` 与 `go test ./...` 全量通过，Linux 版 IM 编译成功。新增 SQL 替身测试覆盖固定内容、各范围冲突、受理重放/失败；Kafka 替身覆盖发送失败、受理保存失败、重构发送器后原事件重试、持久失败不写 broker。生产监听构造使用临时证书和本机真实 TCP gRPC，可信 Agent 加原用户 Token 可重放已受理记录；无 Token、其他同 CA 服务、明文及普通 IM 端口不允许机器人调用。已有普通业务回归通过。

真实 MySQL/迁移、Kafka/Redis、Docker/Compose 合并运行、浏览器、云端证书和模型均未验收。TLS 成功用 SQL 中已受理的测试记录避免请求真实 broker；首次 Kafka 接线由发送器替身测试，不是整个真实消息链。发送器重构保留内存替身不代表真实进程崩溃持久恢复。Linux 编译输出在忽略的 `bin/im-rpc-linux`，不作为源代码修改；沿用现有 Protobuf 工具，没有新增构建依赖。页面未改，本轮未重跑 Node，前一轮 68 项结果不当作本轮浏览器验收。

## Agent 回帖接线与中断收尾（2026-10-03）

这是 2026-10-02 已开始、暂停后在 2026-10-03 继续完成的同一段工作。用户要求“继续完成这一步”，沿用已明确确认的 A44/A45/A46/A47；没有引入新语言、框架、中间件、后台身份或新的服务边界。上轮停在测试补强及部署覆盖调整之后，新测试遗漏 `pb` 导入，文档尚未同步；本次修复缺口并完成收尾，不重新实现或覆盖此前工作。

三个独立小步骤及业务目的：

1. **专用客户端**：Agent 进程增加独立 IMBot mTLS 连接，与普通 IM 上下文/资格查询连接分开；读取私有证书文件，校验明确 IM 名称并出示 Agent 客户端证书。五项全空关闭，部分配置或证书加载失败拒绝启动，不回退明文。连接复用、进程关闭时回收；加载配置不请求模型，也不代表已经成功握手。真实双侧部署凭证尚未准备。
2. **固定回帖记录**：Agent 自有 `agent_task_replies`、015 迁移与新库定义。短事务锁定原运行/草稿，核对持久 `succeeded`、正任务 ID、固定 Task 请求键和原范围，构造实际任务 ID/已确认标题的版本 1 卡片，冻结正文、`bot-task:<run_id>` 和身份/范围。先提交意图再调用 IM；读不到旧记录可以首次准备，已有记录不匹配则拒绝覆盖。网络调用不占事务，受理状态只向成功推进，不保存 Token。SQL 替身不能证明真实 MySQL 崩溃恢复。
3. **确认和显式恢复**：初次确认持久保存 Task 成功后同步尝试回帖。IM 响应丢失或 Agent 受理保存失败，任务依然 `succeeded`；重复确认只读取原结果。GET 无发送副作用，`RetryTaskReply(run_id)` 重查本人 Token、发起人和当前群资格后只重发原回帖，绝不调用 Task 或模型。已受理的重试仍核对当前资格，再返回记录。请求不允许另传正文、任务 ID、机器人身份或群范围；调用方重读/审查后显式操作，本轮没有自动后台恢复。

调用链：

```text
原 Token + 本人确认
  → Agent 查 User / 当前 IM 群资格
  → 冻结 Task 创建意图 → Task RPC → Agent 持久保存任务成功
  → Agent 锁定核对成功结果 → 保存固定回帖意图
  → 专用 mTLS IMBot（转交原 Token，IM 独立校验当前资格）
  → IM 固定发送记录 → 同步 Kafka → IM 保存受理 → Agent 保存受理

GET：只查当前资格与记录
本人 RetryTaskReply：查当前资格 → 原意图 → IMBot；没有 Task 调用
```

Agent 回帖记录与 IM 发送记录各归自己的服务，前者记录已确认任务到群卡片的执行意图/结果，后者记录 broker 发送内容及受理依据。没有跨服务表读写或 MySQL/Kafka/Agent 三方原子事务。若响应不确定，两侧都保留固定消息，可能重复发布同一事件，沿用 A19 的至少一次投递与客户端按 `msg_id` 去重。

RPC 追加 `reply_status`、`reply_msg_id` 字段，保留原字段号及任务成功字段：

| 状态 | 含义及操作 |
| --- | --- |
| disabled | 未启用客户端，不发送 |
| not_started | 尚未形成持久意图，已成功任务可由本人显式开始原回帖 |
| pending | 已保存原意图，Agent 未保存受理；可能 IM 已经受理，先重读再重试 |
| accepted | Agent 已保存 IM 对固定消息的受理；不表示群历史已入库或成员已收到 |
| unknown | 任务成功后的回帖准备失败，无法确定记录状态；保留任务成功，需重读核对 |

读取数据库失败会返回错误，不能伪造未开始或已受理。初次确认在回帖准备失败时保留任务成功与 unknown；如果调用方截止时间已经到达，传输仍可能没有响应，重读持久状态是恢复依据。当前 Gateway 仍只透出任务结果；旧页面不能展示/操作回帖状态，不能据此将任务成功当作回帖成功。独立 HTTP 重试入口及页面操作留下一轮。

请求预算按既定同步方案调整：Gateway 确认路由 19 秒 → Agent 整体 18 秒；创建阶段维持 12 秒（Task RPC 5 秒）＋回帖阶段最多 5 秒（IM RPC 4 秒），为返回留余量；既有 Agent 服务端 20 秒与调用方 21 秒容纳该链路。IM 自身 8 秒上限在 Agent 调用时受更短截止时间约束；较慢权限查询或 broker 仍可能导致不确定结果，保持显式重试，不以超时推断没有发送。其他 HTTP 路由不扩大预算。

可选 Compose 覆盖已追加 Agent 客户端配置，基础文件默认不启用回帖，Agent 仍保留显式 profile。IM 与 Agent 各自只读挂载私有证书目录，没有宿主机机器人端口；准备 013/014/015、机器人资料与完整身份/内容读写链后才在最终部署启用。本轮没有生成部署凭证或启动容器。

验证收尾的第四小步：首次全量回归暴露旧团队群资格测试跨秒误报。旧测试发送与断言时分别生成 JWT，过秒后时间字段不同；又在 RPC 工作 goroutine 中使用 Fatalf，导致等待超时。仅修改 team_group_access_test.go，复用原 Token 验证转发，失败时返回错误而不挂起调用；业务权限代码不改。该场景重复 25 次及修复后的全量 Go 测试通过。

本段跨中断代码、协议、配置、测试与文档共 **27 个文件**，其中 6 个测试文件、2 个生成文件；此前未提交修改保留：

| 文件 | 本段作用 |
| --- | --- |
| [rpc/agent/bot_client.go](../rpc/agent/bot_client.go) | 可选、复用的专用 mTLS 客户端；部分配置拒绝启用 |
| [rpc/agent/bot_client_test.go](../rpc/agent/bot_client_test.go) | 配置拒绝、真实 TLS 名称/身份及 Agent 重试 RPC 接线验证；修复暂停缺失导入 |
| [rpc/agent/draft_reply_store.go](../rpc/agent/draft_reply_store.go) | 核对锁定成功结果，冻结回帖并保存单向受理状态 |
| [rpc/agent/draft_reply_store_test.go](../rpc/agent/draft_reply_store_test.go) | SQL 事务、重放/冲突、范围校验、受理状态及迁移一致性测试 |
| [rpc/agent/draft_reply_rpc.go](../rpc/agent/draft_reply_rpc.go) | 独立回帖编排、只读状态及仅重发原回帖的 RetryTaskReply |
| [rpc/agent/draft_reply_rpc_test.go](../rpc/agent/draft_reply_rpc_test.go) | 任务/回帖结果分离、响应丢失、受理保存失败、权限及重构重试测试 |
| [rpc/agent/server.go](../rpc/agent/server.go) | 接入独立回帖依赖 |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 追加回帖状态字段和显式重试方法，保留原字段号 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 生成追加响应字段 |
| [rpc/agent/pb/agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) | 生成重试 RPC 服务/客户端契约 |
| [rpc/agent/draft_rpc.go](../rpc/agent/draft_rpc.go) | 授权读取后返回独立回帖状态，无发送副作用 |
| [rpc/agent/draft_confirm_rpc.go](../rpc/agent/draft_confirm_rpc.go) | 任务成功持久后同步回帖；独立状态、重复确认只读及总预算 |
| [cmd/agent/main.go](../cmd/agent/main.go) | 生产进程配置、专用客户端接线与连接回收 |
| [cmd/agent/main_test.go](../cmd/agent/main_test.go) | 部分回帖配置在模型构造前拒绝启动 |
| [api/main.go](../api/main.go) | 仅确认路由调整为 19 秒，其他路由保持原预算 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | 新库增加 Agent 回帖记录 |
| [deploy/mysql/migrations/015_agent_task_replies.sql](../deploy/mysql/migrations/015_agent_task_replies.sql) | 已有库的独立 Agent 表增量迁移 |
| [deploy/docker-compose.bot.yaml](../deploy/docker-compose.bot.yaml) | 追加 Agent 证书目录、专用地址与名称，保持显式覆盖 |
| [deploy/.env.example](../deploy/.env.example) | 追加非密钥的私有证书目录和 IM 名称模板 |
| [rpc/im/bot_listener_test.go](../rpc/im/bot_listener_test.go) | 适配双侧覆盖，验证无宿主机端口和独立只读证书挂载 |
| [rpc/im/team_group_access_test.go](../rpc/im/team_group_access_test.go) | 修复旧测试重新签发 Token 的跨秒误报及工作 goroutine 中止后超时 |
| [deploy/README.md](../deploy/README.md) | 双侧证书、015、Agent profile、配置及未验收边界 |
| [rpc/agent/README.md](../rpc/agent/README.md) | RPC 状态、重试语义、预算、权限与持久记录说明 |
| [api/README.md](../api/README.md) | 同步确认预算，说明回帖字段和 HTTP/页面重试仍待接入 |
| [docs/architecture-decisions.md](architecture-decisions.md) | 同步 A44/A45/A46 确认方案的实际进展、代价和验证范围 |
| [docs/project-plan.md](project-plan.md) | 记录三步成果、暂停收尾和真实下一步 |
| [docs/agent-group-reply-design.md](agent-group-reply-design.md) | 完整审查清单、调用链和验证边界 |

验证结果：定向 `go test ./rpc/agent ./cmd/agent ./api ./rpc/im -count=1` 通过；修复旧 Token 测试后 `go test ./rpc/im -run '^TestCheckTeamGroupAccessOverRPC$' -count=25` 与 `go test ./... -count=1` 全量通过。`GOOS=linux CGO_ENABLED=0` 编译 `./cmd/agent` 和 `./api` 成功，产物位于忽略的 bin 目录，未新增依赖。27 个文件定位与链接、Go 格式及差异检查通过（Git 仅提示既有换行转换）。测试以 SQL/业务替身、本机 gRPC 与临时证书验证固定消息重试、任务只创建一次、权限撤销、非法返回、意图失败保留成功任务及证书鉴别；重构实例保留内存替身不代表真实进程崩溃持久恢复。

未验证：真实 MySQL/015 迁移、Kafka/Redis、生产进程与部署证书、Docker Compose 实际合并运行、浏览器及方舟模型。TLS 接线测试中的 IMBot 是本机替身，不是整个真实消息链。没有页面改动，本轮不以先前 Node 68 项结果代替浏览器验收。

## Gateway 与页面回帖接线（2026-10-03）

用户授权“继续进行下一步”，按 A44/A46 和既定原生页面方案完成四个独立小步骤。没有更换框架、服务边界、数据归属或授权方式；没有新增 protobuf、数据库表、迁移或模型请求。前一段 015/mTLS/机器人配置仍是最终启用前提。

1. **HTTP 表达独立状态**：共享草稿响应追加 `reply_status`、`reply_msg_id`。创建成功并非发送成功，pending/unknown 时仍保留原任务 ID。pending/accepted 必须对应已成功运行及其固定消息 ID，其他状态不夹带消息 ID；异常组合返回 502。旧 Agent 省略两字段时保持原响应，页面显示不可用，不推断为未开始或开放操作。
2. **只重试原回帖**：新增 `POST /api/v1/agent/runs/:run_id/reply/retry`，请求体和查询参数为空，仅原 Token 与路径运行 ID转给 Agent。客户端创建键不转发，权限与卡片仍由 Agent/IM 判定。成功须是匹配的受理结果；前提/内容冲突 409，原权限 401/403/404，不可用/超时 503/504，异常成功结果 502。沿用 19 秒路由容纳 Agent 18 秒预算，不增大其他路由。
3. **页面明确恢复**：任务成功与回帖状态分开显示。确认后 pending/unknown 不自动重读或重发；本人点击 Load 后，仅 not_started/pending 开放“Send group reply”/“Retry group reply”。操作互斥、双击不多发，失败保留精确任务 ID，再次成功重读才恢复操作；已受理、关闭、未知及旧字段缺失都不开放重试。切换 Token、群或运行使旧响应失效，即使切回原上下文也不显示为当前成功。标题、状态、ID 用纯文字显示，复用现有页面样式。
4. **本机调用链验证**：HTTP 处理器、真实 Agent 编排/SQL 适配和生产 mTLS 客户端接在一起；临时 CA/独立客户端与服务端证书用于 TLS 机器人替身。初次回帖响应丢失、显式重试受理保存失败、本人重读后同消息重发、已受理重放、再次确认以及撤销权限均验证；Task 业务替身的创建调用数始终为一。没有真实模型或 broker，没有把机器人替身当作生产 IM 验收。

```text
页面 Confirm → Gateway → Agent 保存 Task 成功 → 同步 IMBot 回帖
                    ← 成功任务 ID + 独立 pending/accepted/unknown

页面 Load → Gateway GET → Agent 当前资格 + 持久状态（无发送）

页面 Send/Retry reply → Gateway 空体 POST → Agent RetryTaskReply
                     → 当前本人/团队群检查 → 原持久意图
                     → 专用 mTLS IMBot → 保存受理
                     ← 同任务 ID + 同消息 ID + accepted
```

`accepted` 表示 IM 已受理；页面明确不确认全部成员送达。`pending` 可能 IM 已受理但 Agent 未存下 ACK，不能把它当作“没有发送”；依赖 A47 固定事件与 A19 客户端消息去重。HTTP 重试错误不否定原任务或先前发送，页面保留任务成功并要求重读。若失败后的 Load 也被拒绝，不恢复重试操作。

普通实现取舍已关联 A46：选择共享结果校验而非裸透传，避免未知状态/错消息伪报成功，代价是异常结果需要重读；选择旧缺字段时兼容但禁用操作，而非默认 not_started，避免旧服务误开放重试；选择只接原运行的空体接口，而非收取新卡片，确保重试固定意图；沿用明确 Load 后手动操作，而非自动重发。均是用户确认方案内的局部契约/交互，不引入新的架构选型。失败恢复仍需要本人当前权限。

新增页面测试中，模拟旧响应最初遗漏删除消息 ID，页面正确拒绝不一致字段；修正测试数据后所有 77 项通过，没有放宽结果校验来迁就测试。

全部实际修改共 **14 个文件**，其中 4 个测试文件；保留已有未提交工作：

| 文件 | 本段作用 |
| --- | --- |
| [api/agent_draft.go](../api/agent_draft.go) | 共享响应追加回帖状态/消息 ID，拒绝矛盾结果并兼容旧字段缺失 |
| [api/agent_draft_reply_test.go](../api/agent_draft_reply_test.go) | 任务成功独立保留、精确 ID、旧响应及非法组合测试 |
| [api/agent_reply_retry.go](../api/agent_reply_retry.go) | 只含原运行和 Token 的显式回帖重试 HTTP 处理器 |
| [api/agent_reply_retry_test.go](../api/agent_reply_retry_test.go) | 请求输入、原凭证转发、错误屏蔽与异常受理拒绝测试 |
| [api/main.go](../api/main.go) | 注册独立回帖重试路由，19 秒预算 |
| [api/agent_reply_flow_test.go](../api/agent_reply_flow_test.go) | HTTP→实际 Agent→mTLS 客户端/机器人替身的持久流程验证 |
| [examples/chat.html](../examples/chat.html) | 独立状态、先读后手动重试、互斥、失败保留任务与上下文保护 |
| [examples/chat.test.cjs](../examples/chat.test.cjs) | 新增九项回帖场景，模拟页面字段与网络结果 |
| [api/README.md](../api/README.md) | 回帖响应、空体接口、错误、预算与页面交互说明 |
| [rpc/agent/README.md](../rpc/agent/README.md) | 同步 HTTP/页面接入状态与兼容边界 |
| [deploy/README.md](../deploy/README.md) | 同步实际入口和部署验收前提 |
| [docs/architecture-decisions.md](architecture-decisions.md) | 更新 A46 实施状态，记录接口/交互取舍与替身验证边界 |
| [docs/project-plan.md](project-plan.md) | 本轮四个实际小步及下一处设计讨论 |
| [docs/agent-group-reply-design.md](agent-group-reply-design.md) | 调用链、全量验证与完整文件清单 |

验证：`go test ./api -count=1`、`go test ./... -count=1` 全量通过；`node --test examples/chat.test.cjs` 77/77 通过。更新后的 Gateway 以 `GOOS=linux CGO_ENABLED=0` 编译成功，输出在忽略的 bin 目录；Go 格式及 JS 测试文件语法检查通过。14 个文件定位、审查链接及差异检查通过。

未验证：真实浏览器、MySQL 及 015 迁移、生产 IM/Kafka/Redis/Push/WS 的完整新消息链、生产证书、Docker/Compose/云端及方舟模型。联调的 IMBot、User/普通 IM/Task 和 SQL 是业务/存储替身；mTLS 握手与 RPC 编解码为实际本机通信。测试没有模拟全量慢依赖/压力，超时数值是已有同步方式下的请求预算，不能视为生产延迟保证。阶段 6 的负责人匹配、模糊时间、多项草稿及群内 @AI 尚未实现；下一处负责人规则/接口设计先讨论后实施。


