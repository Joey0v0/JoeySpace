# Agent 持久通知接收：共同契约

2026-10-04。沿已批准 A57/A59/A61，用户本轮明确选择 A62：异常通知停止触发消费、保留未确认offset，需要修正后重启；本人操作RPC继续服务，不静默丢弃或创建隔离/DLQ。本轮只接收排队，不做租约/模型/草稿/Task/回帖。

## 最小状态与存储

- Agent拥有新表 agent_task_trigger_inbox，主键 message_id；只记录 action、event_version、status=queued、服务器received_at。023与初始化表一致，迁移本批不执行。queued只表示通知已持久接收，不等同合法用户权限/正在处理/模型完成。后续处理仍从受限IM接口派生资格/指令/原消息参考时间。
- 不保存Token、客户端metadata、事件正文副本、任意actor/team/group，不读IM/User库，不修改agent_runs/drafts或旧IM Outbox。received_at不用于解释“明天”；后续用原消息参考。
- TriggerInbox.Accept(ctx, model.AgentTriggerEvent) error；NewTriggerInboxStore(db) *TriggerInboxStore。先validateTriggerNotification固定action/version/正ID，nil ctx或无效参数InvalidArgument，取消/期限优先、无DBUnavailable，3秒SQL预算。
- 短SQL事务INSERT固定不可变事实和queued，RowsAffected须恰好1。只识别MySQL1062重复；同事务SELECT message_id/action/event_version/status/received_at FOR UPDATE，严格Scan拒绝NULL、多行、缺失、非法timestamp/状态和不同事实；相同重复只读后commit，不UPDATE、不重置时间/状态、不重新生成。其他INSERT/查读/提交失败safe Unavailable；坏保存事实safe FailedPrecondition；全部不泄漏SQL/内容/凭据。
- 当前仅queued状态，未来工作状态扩展必须更新读取兼容/持久租约设计；本批不先造空跑worker或消耗A61模型次数。数据库约束负责并发去重；SQL替身不等同真实并发MySQL验收。

## 严格通知和确认顺序

- DecodeTriggerNotification(message kafka.Message) (model.AgentTriggerEvent,error)。body≤4096字节、合法UTF8、顶层JSON object、恰有 version/action/message_id 三字段，未知/缺字段/重复键/尾随第二对象拒绝。version JSON整数1，action固定extract_tasks，message_id规范正十进制字符串（无空白/零前缀/符号/指数/浮点，int64不溢出）。Kafka Key必须等于agent-trigger:tasks:<来源ID>，与现有publisher一致；不信任额外Header身份，不向RPC或存储传header。
- TriggerEventReader接口已统一。NewTriggerConsumer(reader, inbox) (*TriggerConsumer,error)、Run(ctx) error、Close() error（nil安全/幂等）。内部 reader/inbox/retryDelay（生产1秒，tests可调短）；单goroutine串行Fetch→Decode→Accept→同步Commit。一条未保存/未确认期间不得Fetch更高offset，以免Kafka组累计确认越过失败记录。
- Decode错误返回ErrInvalidTriggerNotification，不Accept/不Commit/不继续Fetch；持久事实FailedPrecondition返回ErrInvalidTriggerInbox，同样停消费。无消息正文/Key/原错误泄漏。只停此触发consumer，RPC继续。A62不要改成自动跳过或补默认字段。
- Fetch、暂时SQLUnavailable、Commit失败均可取消间隔重试；保存失败只重试同条Accept，成功后Commit失败只重试该条Commit，不再Accept、不提前Fetch。不消耗模型尝试次数。每个Commit≤3秒；父取消优先，Run返回caller取消/期限而非成功；最终Close由进程在取消并等Run结束后调用。Kafka ACK不确定后重启重放，同源主键保证相同记录，无第二行或状态重置。
- 本批不调用TriggerContextClient/任何模型/Task/RPC，只消费新的专用通知主题。尚未开启消费者/Outbox或真实Kafka联调。

## 可选进程接入

- AGENT_TRIGGER_INBOX_ENABLED只接受空/false（禁用）或true，空白/其他值拒绝。禁用不读取/构造未用Kafka配置或访问新表，保留已有本人同步RPC/模型配置。
- 启用需 AGENT_TRIGGER_KAFKA_BROKERS（逗号合法非空host:port）、AGENT_TRIGGER_KAFKA_TOPIC、AGENT_TRIGGER_KAFKA_GROUP。严格非空无空白，Topic遵Kafka命名、不得chat_messages；group与原聊天消费im_push_group不同，消费通知的专用组。必须有Agent DB；配置在连接DB前检查，023须用户最终部署时执行。
- Kafka Reader固定GroupID、CommitInterval=0（同步确认）、MinBytes1/MaxBytes64KiB、StartOffset=kafka.FirstOffset，避免新组初次跳过已经发布的通知；依赖Kafka保留期，旧已published记录本批不回扫。复用已有kafka-go，无新依赖。
- cmd私有loadAgentTriggerInboxConfig/newAgentTriggerInbox（允许注入reader factory测试），仅启用构造Agent.NewTriggerInboxStore+NewTriggerConsumer。配置/工厂/资源错误safe，不打印连接字符串或值。Runtime用父ctx派生cancel，后台Run的异常安全报告后只停止消费，不Stop普通RPC；记录可供后续诊断，本批无新诊断API。
- main接只读配置、consumer与停止函数；正常结束取消→等Run结束→Close reader，之后释放RPC客户端和DB；构造失败显式清理已准备资源，避免log.Fatal跳过defer。未启动consumer也关闭准备reader；不无关改newAgentServer/Ark/manual方法，不请求真实模型。

## 分工和九步

1 root：rpc/agent/trigger_inbox_contract.go、023、init.sql、本契约，共同提交。
2–3 A D:/zy/GoLang/go-im/.worktrees/assignee-backend：只新增rpc/agent/trigger_inbox_store.go/test.go；SQL存储/不可变去重及失败测试。
4–5 B D:/zy/GoLang/go-im/.worktrees/assignee-gateway：只新增rpc/agent/trigger_notification.go/test.go、trigger_consumer.go/test.go；严格解析＋顺序持久确认与A62失败测试。
6–7 C D:/zy/GoLang/go-im/.worktrees/assignee-ui：仅cmd/agent/main.go、新trigger_inbox.go/test.go；默认禁用配置/reader工厂与可取消进程接入/失败不影响本人RPC。
8 root：rpc/agent/trigger_inbox_flow_test.go实际GORM/SQL替身+consumer组合，故障/重放/无重复，schema一致检查。
9 root：集中全量Go、Linux Agent编译、ADR/计划/协作/审查全文件。三个执行agent仅gofmt/diffcheck，不tests/build/审批/提交/合main/push，不越界或改协议/生成/迁移/依赖/docs。

下一批A61租约/模型两次预算；后台负责人解析仍须受限当前资格接口。旧Task/回帖本人确认保持。
