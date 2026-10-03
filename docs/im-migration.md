# 阶段 3 第一步：现有群聊链路与接入点

第一小步先阅读代码并确定改造顺序；第二小步已在旧群聊历史查询前增加成员资格检查；第三小步建立了独立 IM RPC 的群成员查询入口；第四小步让 WS 在旧群消息入队前调用该 RPC。团队（`teams`）是协作范围；群（`groups`）是聊天会话，不能把团队 ID 当作群 ID 使用。本地已准备团队群的增量字段，真实数据库尚未执行迁移。

## 团队群的数据边界

IM 拥有 `groups` 与 `group_members`。本地新库初始化脚本为 `groups` 增加可空 `team_id` 和 `(team_id, id)` 索引；已有数据库使用 [003_group_team_id.sql](../deploy/mysql/migrations/003_group_team_id.sql) 一次性迁移。旧群的 `team_id` 保持 `NULL`，未来团队群使用非空团队 ID；旧群接口和成员表本步不变。该字段没有指向用户与团队服务 `teams` 表的外键，避免 IM 直接依赖其他服务的数据库结构。

非空 `team_id` 不能仅凭请求参数写入：用户与团队 RPC 已提供 `AuthorizeTeamGroupCreation(team_id)`，由登录 Token 确定操作人，只有目标团队的拥有者可通过；基于 `team_members` 与 `teams` 的外键关系，拥有者成员记录也表明团队存在。API Gateway 已提供团队群创建 HTTP 入口，转发给 IM RPC `CreateTeamGroup`；IM 先调用授权接口，再用事务写入团队群和群主成员。创建请求现在携带 `Idempotency-Key`，IM 以 `(owner_id, request_key)` 唯一约束防止并发重试产生重复群；同键不同团队或群名返回冲突。需执行 004 增量迁移；真实数据库联调仍未完成。团队群目录通过 IM `ListTeamGroups` 查询：先向用户与团队 RPC 核对当前团队成员身份，再按团队 ID 和群 ID 分页读取；团队群列表的可见性不等于群成员资格。IM `CheckGroupMember` 对团队群会调用用户与团队 RPC 的 `CheckTeamMember(team_id)`，重新核对当前团队资格；旧群只查群成员。旧开放加入群接口和成员列表接口均拒绝团队群；旧“我的群”列表只查询 `team_id IS NULL` 的群；旧 HTTP 历史接口也拒绝团队群，避免绕过团队资格检查。团队成员资格与群成员资格仍是两项不同检查。团队群历史现由 IM RPC 与 Gateway 提供，读取前核对群成员和当前团队资格，并核对请求中的团队与群归属；已有库需执行 005 索引迁移。真实数据库及团队群发送、推送链路仍未完成联调。

## 当前调用链

| 动作 | 实际路径 | 当前校验 |
| --- | --- | --- |
| 群消息发送 | `internal/ws/server.go` 验证连接 Token → `internal/ws/client.go` 在 `chat_type=2` 时携带 Token 调用 IM RPC → Redis 预约 `msg_id` → 写 Kafka → Redis 确认为已入队 → 回复 ACK | IM RPC 联查群成员与 `groups.team_id`；团队群额外调用用户与团队 RPC 检查当前团队资格。非成员、凭证无效或 RPC 故障时不去重、不入队、不回复 ACK。单聊不调用此 RPC |
| 消费与推送 | `internal/push/consumer.go` 先 Fetch、处理成功后提交偏移量 → `internal/push/pusher.go` 先写 `messages`，再从 MySQL `group_members` 读取当前名单，逐人推送或保存离线消息 | 处理返回错误会重试当前消息；名单查询失败不使用旧 Redis 缓存投递；群成员列表用于确定接收者，不验证发送者；查询接收者发生在消息入库之后 |
| 历史查询 | 旧 Gin `internal/handler/message_handler.go` 取登录用户和参数 → `internal/service/message_service.go` → `internal/repository/message_repo.go` 按 `chat_type=2`、`to_id=群 ID` 查 `messages` | 消息服务层先核对 `group_members`，再读取群归属；旧群成员可读，团队群从此旧入口拒绝；单聊路径不变 |
| 群管理 | `internal/handler/group_handler.go` → `internal/service/group_service.go` → `internal/repository/group_repo.go` 操作旧 `groups`、`group_members` | 旧“加入群”和成员列表接口均拒绝团队群；旧群成员列表先检查请求人属于群；旧“我的群”列表只展示 `team_id IS NULL` 的群 |

仍需按后续小步验证的可靠性边界：WS 的 ACK 表示 Kafka 写入成功且 Redis 已确认入队，**不表示**消息已落库或推送成功。Kafka 写入结果不确定、Redis 确认失败或预约过期时，客户端重试可能再次写 Kafka。Kafka 消费已改为处理返回成功后再提交；群成员个别投递失败现在也会向消费层返回错误，但重试会重新遍历全群，已成功的在线成员可能再次收到相同 `msg_id` 的消息。进程在处理成功与提交之间退出时也可能重放。用户已选择保留至少一次投递，由客户端按 `msg_id` 去重；现有 [WebSocket 演示页](../examples/chat.html) 已在当前页面会话内按 `msg_id` 合并实时、离线与历史消息，最近保留 5000 个 ID；刷新后去重集合重置，正式聊天页面仍待开发。内部推送接口在客户端写队列满或连接已关闭时返回 503，Push 随后保存离线消息；接口返回 200 仅表示写队列接收，不表示客户端收到，队列入列后的网络故障仍可能丢失实时投递。这些风险尚无端到端运行验证。

## 渐进接入顺序

1. **已保护现有历史接口**：旧 `MessageService.GetHistory` 的群聊分支在查询历史前核对请求者的旧 `group_members` 记录。成员可读；非成员返回旧接口业务码 `40003`，不触发历史查询；数据库故障也不放行。单聊和旧群结构不变。本地服务与 Handler 测试通过，真实 MySQL 尚未验证。
2. **再建立 IM 业务边界**：已新增独立 `CheckGroupMember` RPC，收到旧群 ID 后自行验签 `authorization: Bearer <Token>`，从 Token 取得用户 ID 并查询旧 `group_members`。成员返回成功；非成员返回 `PermissionDenied`；无效凭证和数据库故障分别返回 `Unauthenticated`、`Unavailable`。接口不接受调用方填写的用户 ID。当前有本地 SQL 测试替身和 gRPC 调用测试，尚未连接真实 MySQL，也没有检查用户账户是否后来被禁用。后续逐步把群归属、消息读写移到 IM 服务；新群需明确关联团队，群成员资格和团队成员资格分别校验。
3. **发送入口已接权限校验**：WS 从已验证的连接保存登录 Token；群消息先调用 IM RPC，成功后才使用 Redis 去重及 Kafka。无成员资格返回错误码 403，Token 在连接期间失效返回 401，RPC 未配置或不可用返回 503；均不回 ACK。单聊路径仍不做群成员检查。启动 WS 时设置 `IM_RPC_ADDR=127.0.0.1:9002`（容器内需使用 IM 服务名）；未设置时旧群发送会被拒绝。当前通过 WS 替身测试验证调用顺序与失败关闭行为，尚未做 WS、IM RPC、Redis、Kafka 的真实联合运行。
4. **消费确认的前置准备**：现有 `messages.msg_id` 有唯一约束，但同一 Kafka 消息重试入库会因重复键报错。现在消息 Repository 在 MySQL 返回重复键时按 `msg_id` 查回已有记录；只有发送者、目标、类型和内容均相同才复用原记录 ID，内容不同则拒绝。SQL 测试替身通过。消费者后续已改为 `FetchMessage`，处理成功后才提交；真实 Kafka 联调仍未完成。
5. **离线记录去重**：新建数据库的 `offline_messages` 增加 `(user_id, message_id)` 唯一约束；已有数据库需先检查重复记录，再执行 [增量迁移](../deploy/mysql/migrations/002_offline_message_unique.sql)。Repository 收到该唯一键的重复错误时，查回同一用户与消息的原记录并复用其 ID；其他数据库错误继续返回。SQL 测试替身通过，迁移尚未在真实 MySQL 执行。在线推送仍可能重复。
6. **处理后确认 Kafka**：消费组使用 `FetchMessage` 取消息，Push 返回成功后才同步调用 `CommitMessages`。处理失败时每秒重试同一消息，不取下一条；提交失败时只重试提交，不在当前进程中重复推送；取消时停止，未提交消息可在重启后重放。无法解析的消息会记录错误并提交跳过，避免坏消息永久阻塞。替身测试覆盖这些顺序。此处是至少一次处理：进程崩溃或分区重分配后仍可能重复在线推送；下游应按稳定的 `msg_id` 去重。
7. **群成员失败向上传递**：Push 给群成员逐个投递时，单个失败不会阻断后续成员，但会汇总返回错误；消费层因此保留当前 Kafka 消息并重试。本地测试验证某个成员失败后，其他成员仍得到处理，且总体结果为失败。重试整个群可能重复推送已成功的在线成员；离线记录的唯一约束需要先在真实数据库完成迁移，不能只凭代码宣称端到端幂等。
8. **发送端去重状态**：Redis 中的 `msg_dedup:{msg_id}` 先存随机预约者，Kafka 写入成功后才原子改为已入队状态；只有已入队且指纹相同的重复请求回 ACK。其他请求看到发送中状态返回稍后重试，写 Kafka 失败则仅由预约者删除键。Kafka 写入设置 10 秒超时，预约键保留 5 分钟；如果 Redis 确认失败，返回状态不确定，不回 ACK。WS 替身测试覆盖失败后重试、发送中、已确认重复和确认失败；尚未使用真实 Redis/Kafka 联调。旧版值 `1` 或上一小步的 `sent` 会按发送中处理，等原有 TTL 到期后可重试。
9. **同 ID 不同内容拒绝**：WS 用服务端用户 ID、接收目标、聊天类型、内容类型与内容计算指纹，不包含每次重试变化的时间戳。Redis 预约和已入队值都携带该指纹；相同 `msg_id`、不同指纹返回 409，不写 Kafka，也不回 ACK。进入权限查询和 Redis 前先校验消息 ID 长度 1～64、目标 ID 为正数、聊天类型为单聊或群聊、内容类型为已支持的 1～3。本地替身测试通过；Redis 原子脚本和新旧键过渡尚未做真实环境验证。

IM RPC 的本地启动方式见 [IM RPC](../rpc/im/README.md)。发送入口的成员校验代码和 Compose 服务配置已接通，但尚未实际构建、启动容器或做真实全链路联调。消息可靠性和团队群仍未完成。真实数据库、Redis、Kafka、容器联调依照项目方案，留到整体完成后统一同步部署时验收。

团队群现在增加了本人自行加入入口：`POST /api/v1/teams/{team_id}/groups/{group_id}/join` → IM `JoinTeamGroup` → 用户与团队 RPC 核对团队资格 → IM 核对群归属并写 `group_members`。旧群与其他团队的群不能通过此入口加入，重复加入返回成功。Push 每次群投递从 MySQL 查询当前 `group_members`，避免一小时 Redis 缓存使新成员漏收；旧群也按此方式读取。用户已确定未来离队即退出该团队全部群，再次入队需自行加入；当前没有离队接口，因此清理旧群资格尚未实现。真实数据库、Kafka 和 WS 的端到端投递仍待统一验收。

离线消息采用客户端显式确认：旧 Gin 与新 Gateway 的 `GET /api/v1/message/offline` 均只读，未确认消息可重复拉取，响应中 `id`、`from_id`、`to_id` 均为字符串。[WebSocket 演示页](../examples/chat.html)现由 Gateway 提供，在连接成功后带原 Token 拉取离线消息，按 `msg_id` 合并实时与离线消息，再调用同源 `POST /api/v1/message/offline/ack`；请求体如 `{"message_ids":["9007199254740993"]}`，一次最多 1000 个正整数 ID 字符串。确认只删除登录用户对应的离线记录；重复确认成功。确认失败后重连会重新拉取，演示页在当前页面会话内不重复展示。旧 Gin 接口保留作过渡，Gateway 经 IM RPC 承接读取和确认。Node 模拟测试已覆盖合并、确认失败重试和分批确认；页面刷新会清空去重集合，真实浏览器、MySQL 与容器联调尚未完成。
