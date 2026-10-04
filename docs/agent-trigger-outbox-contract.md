# IM @AI 可靠通知共同契约（2026-10-04）

沿用户明确选择 A57 B、A58 A、A59 A、A60 A、A61 A。本批只完成 IM 消息/事件同事务与 Kafka 发布，不提供 Agent 消费、服务 RPC、模型或草稿查询入口；最多九步。022 未执行。默认开关关闭，未准备好后续消费者/受限读取时不启用。

## 共同形状和边界

model.AgentTaskCommand 只接受用户团队群文本的严格首部命令；词间允许空白，指令原文 trim 后 1—2000 Unicode 字符；无效命令仍普通聊天。机器人、私聊、旧非团队群、引用或正文出现不触发。旧 sender_type=0 视用户，但 initiator_id 必须0。团队 ID 来自 IM groups，不从聊天客户端读，更不跨库读取 User 团队表。当前资格由后续 A60 服务 RPC 重新核对，本批快照不是授权。

model.AgentTriggerOutbox 一条消息仅有 extract_tasks 动作；message_id 主键，完整原始 MsgID/作者/团队/群/指令和 reference_time_ms 固定。保存源消息后必须在同事务按其 ID 重读数据库时间（messages TIMESTAMP 只有秒精度），禁止用进程内纳秒/重试时刻生成参考；重复消息沿原 Create 内容比较再复用数据库 ID。完整事实冲突拒绝，不能 ON DUPLICATE UPDATE 偷改意图。

Kafka JSON 仅 version=1/action=extract_tasks/message_id（十进制字符串）。Key 固定 AgentTriggerOutbox.RequestKey()；无用户 Token/命令正文/自报角色。事件不是授权证明。未来 Agent 按固定键去重，受限 RPC 读原始持久事实与当前资格；Task 创建/回帖仍本人确认。Published 仅 Kafka ACK，不表示生成或送达。

## 三个执行模块的固定 API

事务模块：新 internal/repository/agent_trigger_repo.go/test.go。NewAgentTriggerMessageRepository(db *gorm.DB) MessageRepository，嵌入原 messageRepository 复用其余方法，只覆写 Create；不改旧接口/旧 Create。普通消息直接走旧 Create。候选事务：原 Create→重读已保存源消息→群 TeamID（不存在/旧群不触发）→完整记录构造及 Validate→插入 Outbox→提交；失败回滚全部。数据库重复 Outbox 时重读并比较全部不可变字段（Published 可不同），禁止更换范围或原文；允许相同已入库命令在 Kafka 重放时补其唯一事件，不扫描历史。

同文件提供 NewAgentTriggerStore(db *gorm.DB) AgentTriggerStore。ListPending(ctx,limit) 限1—100，published=false、message_id 升序；坏记录拒绝。MarkPublished(ctx,row) 必须完整比较锁内持久意图后更新，已 published 的相同事实幂等；不存在、异事实、影响非1行拒绝。不得锁数据库跨 Kafka；使用原子短事务/行锁。本批发布器允许并行重复通知，由未来 Agent 幂等，勿宣称 exactly-once。

发布模块：新 internal/push/agent_trigger_publisher.go/test.go。AgentTriggerWriter 接口 WriteMessages(context.Context,...kafka.Message) error、Close() error。NewKafkaAgentTriggerWriter(brokers []string,topic string) AgentTriggerWriter 返回同步 kafka.Writer（RequireAll、禁止 Async）。NewAgentTriggerPublisher(store repository.AgentTriggerStore,writer AgentTriggerWriter,logger *zap.Logger) *AgentTriggerPublisher；RunOnce(ctx) error 拉至多100条，Validate、固定 JSON/key，最多5秒一次 Write、ACK 后 MarkPublished，每条尊重取消；任何故障不标发布，返回错误。Start(ctx) 定期 RunOnce，轮询/失败等待约1秒、可取消；SQL/网络轮次必须有界（建议每轮10秒）。失败不泄露正文或身份资料。Close writer 由进程拥有，Start 不替调用者 Close。

进程模块：仅 cmd/push/main.go、新 cmd/push/agent_trigger.go/test.go。KafkaConfig.AgentTriggerEnabled / TopicAgentTrigger 已由root定义，默认false。启用前 ValidateAgentTriggerConfig 或本包等价 helper 先验证 brokers/topic 非空，主题严格不同于聊天主题（禁止留空让Writer写回聊天）。关闭时不创建 store/writer、不改普通消息存储、不查询新表。启用才组装事务仓库/发布器。提供可替换 factory/runner 的小接入 helper 验证开启/关闭与生命周期；进程在共享 context 启动 publisher，退出先 cancel 并等待消费者及publisher退出，后 Close 资源，避免 goroutine 在已关闭writer上写。依旧不启动 Agent worker。

## 协作范围和验证

共同1、事务2、发布2、接入2、root流程1、审查1=九步。三原 worktree 从同一共同提交开 codex/agent-trigger-store、publisher、runtime；各模块仅上述允许文件，禁止改模型、迁移、配置样例、协议/generated/依赖/共同文档或自行提交/合并。仅编辑/gofmt/diffcheck，不运行 Go 测试/build 或请求测试审批。root 统一提交、整合、集中测试。

至少覆盖：命令过滤、真实 GORM/MySQL SQL 替身原子提交/回滚、重复复用源时间与事实冲突、Kafka 成功/失败及 ACK 保存失败重发同 bytes/key、取消、默认关闭、错误主题和生命周期。root补真实 Pusher→事务存储→publisher 组合；MySQL/Kafka仍替身，真实迁移/崩溃恢复/并发与容器云部署不标完成。Outbox 发布可不断重试，不消费 A61 的两次模型尝试预算，A61 在后续 Agent 持久执行实施。
