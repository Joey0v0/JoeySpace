# Agent → IM 持久触发上下文：共同契约

日期：2026-10-04。沿用户已确认 A57—A61，尤其 A60；只读范围，不生成草稿或创建任务。最多九步，root 统一协议、整合及验证；三 worktree 从共同提交开始。

## 只读范围

- 新独立 IMTrigger.ReadTaskTriggerContext，request 只有 message_id。不接受 actor/team/group/token/游标/调用方 reference；新协议文件和生成文件，不改旧 IM 或 Agent 协议。
- 先 RequireServiceIdentity(ctx, 配置的精确 Agent DNS SAN)，包括错误注册在明文端口时；无登录 Token 兜底。总读取超时 5 秒，User 客户端维持原 2 秒。取消/超时优先，错误不泄漏 SQL/内容/凭据。
- 从 im_agent_trigger_outbox 按 ID 读取唯一行并 Validate，再从 messages 验证 ID、msg_id、FromID、ToID、用户文本群消息、InitiatorID=0、解析出的指令、CreatedAt.UnixMilli 等于保存事实；NULL、重复、无效事实拒绝。published=false 也可读：Kafka 已 ACK、数据库标记尚未完成的窗口不能拒绝合法事件。
- 核对 groups.team_id 与保存 TeamID 一致、当前 group_members 有原发起人；向受限 UserTrigger 检查保存 actor/team 当前成员和账户。只用 triggerTeamEligibility.Check，禁止调用旧需要 Bearer 的 IM 权限函数、跨读 User 表或伪造 JWT。
- 在 User 通过后，历史查询再次在 SQL 中限制当前 groups.team_id/group_members.user_id，to_id=保存群、chat_type=2、id <= 来源ID，倒序最多20条。无资格不得返回任何正文；列表第一条必须与刚核对的来源一致，避免查询间来源更改。返回规范化 sender_type（旧0映射用户1）、原 InitiatorID/正文/时间。原记录和历史均用显式 Rows.Scan 或同等严格 NULL 拒绝；不静默补默认值。
- 响应 ID/用户/团队/群/指令/时间/key 都来自存储，key=agent-trigger:tasks:<十进制消息ID>。来源时间使用服务器保存的时间，不用工作线程当前时间；重试相同ID上界不纳入新讨论，但不是完整历史快照，也不声称跨服务原子授权。无缓存，每次请求重查当前资格。
- 历史每条正文合法 UTF-8、至多 MySQL TEXT 的65535字节，字段ID/时间正值，用户消息 InitiatorID=0，机器人消息 InitiatorID>0；发送者只1/2，消息类型沿现有1..4。总响应 <=2MiB（protobuf size），避免不受限读取。客户端也限制2MiB及20条，核对降序唯一ID、来源首条、来源解析/身份/时间与响应完全对应，不信任异常成功返回。
- 错误：身份失败 Unauthenticated；请求ID无效 InvalidArgument；无持久触发/来源 NotFound；当前团队/群或归属失效 PermissionDenied；存储事实不一致/无效/DB不可用 Unavailable；取消/超时维持对应码。客户端保留这些预期码，其余映射 Unavailable，安全通用文案。不自动 RPC 重试，不消耗 A61 模型尝试次数。

## 独立监听与客户端

- IM配置：IM_TRIGGER_LISTEN_ON、IM_TRIGGER_AGENT_DNS_NAME、IM_TRIGGER_TLS_CERT_FILE/KEY_FILE/CA_FILE。全部空禁用，部分/空白/错误配置拒绝启动；单独端口且与普通IM、已启用bot端口不同。监听生产端口1..65535，测试构造允许0。TLS复用 rpcauth.NewServiceServerCredentials，仅注册 IMTrigger，MaxRecv4096，无reflection。
- IM启用必须完整上批 IM_TRIGGER_USER_* 配置；root提供既存 newTriggerTeamClient。未启用新监听时保留旧进程行为。新监听构造 newIMTriggerRuntime(c, db, teams)，返回持有 server/listener 的 runtime、幂等 Stop；不拥有/关闭DB。loadIMTriggerConfig(getenv)、validateIMTriggerStartup(c,ordinaryAddr,botAddr) 由监听agent实现。
- IM main 接入新listener和User客户端，错误时关闭已准备资源。不把 zrpc.Stop 误作停止RPC（它只关日志）。按上批 User 方式：普通服务器 registration callback 发布实际 *grpc.Server，再启动 Trigger.Serve；任一新增监听故障停止实际普通 server，主函数返回执行清理，不等待Linux go-zero的额外shutdown通知。waitIMTriggerServers(startOrdinary,ordinaryReady,triggerFailed) 同型辅助；已存在bot链路只作必要并存，禁止无关重构。
- Agent配置：AGENT_IM_TRIGGER_ADDR、AGENT_IM_TRIGGER_SERVER_NAME、AGENT_IM_TRIGGER_TLS_CERT_FILE/KEY_FILE/CA_FILE。全部空禁用nilclient，无明文回退；部分配置拒绝。NewTriggerContextClient(getenv) (*TriggerContextClient,error)，Read(ctx,messageID) (*impb.ReadTaskTriggerContextResponse,error)，Close() error 幂等/nil安全。内部rpc字段 impb.IMTriggerClient、conn *grpc.ClientConn；清空所有调用方 outgoing metadata，5秒总超时，NewServiceClientCredentials，禁用RPC重试，关闭连接映射Unavailable且不盖调用方取消。不接 Agent main/模型，本轮为后续 worker 提供有界入口。

## 文件授权与步骤

1 root：此契约、rpc/im/trigger.proto、pb/trigger*.go、trigger_context_contract.go，共同提交。
2–3 后端 D:/zy/GoLang/go-im/.worktrees/assignee-backend：只新增 rpc/im/trigger_context.go/test.go；来源读取/当前资格＋有界历史及失败测试。
4–5 监听 D:/zy/GoLang/go-im/.worktrees/assignee-gateway：只 rpc/im/main.go、新 trigger_listener.go/test.go；进程接入＋TLS/生命周期测试。不得改bot实现、User客户端或协议。
6–7 客户端 D:/zy/GoLang/go-im/.worktrees/assignee-ui：只新增 rpc/agent/trigger_client.go/test.go；有界只读＋证书/异常响应测试。
8 root：组合实际TLS/SQL替身测试 rpc/im/trigger_context_flow_test.go，检查并整合。
9 root：集中全量Go、Linux IM/Agent编译，更新ADR/计划/协作文档/全文件审查。子agent仅编辑/gofmt/diffcheck，不测试/build/提交/合并/审批/push。不得修改迁移/依赖/共同文档/其他允许范围外文件。

本轮不开 Outbox 开关，不执行022迁移，不访问真实MySQL/Kafka/模型/云。下一批仍需 Agent 持久接收/租约、最多两次生成及本人页面入口。
