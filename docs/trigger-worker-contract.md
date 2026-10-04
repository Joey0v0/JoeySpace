# A60/A61 后台整理任务 worker 契约

日期：2026-10-04。用户已选持久退避（本轮回复），沿 A57/A58/A60/A61：IM 已保存命令触发 Agent；只生成本人待审草稿，不自动创建 Task 或回帖。模型仍用已有 Eino 适配器，测试使用本地替身，真实方舟调用待接入点和预算确定。

## 执行链及边界

1. 单进程先启动普通 Agent RPC 与既有 Kafka 通知接收；独立可关闭的 worker 串行 `Claim` 一条 inbox 租约。空队列短暂等待，不紧循环。租约30秒，工作期间每10秒 `Renew`；续租失败取消本条处理并停用旧持有者写入。worker停机可取消，在途处理结束或租约自然到期，不释放他人租约。普通本人 RPC 不依赖 worker 健康。
2. 处理器用现有专用 mTLS `TriggerContextClient.Read(message_id)` 从 IM 已保存事实取得 actor/team/group、指令、原消息服务器时间及最多20条消息。此调用重查当前 User 团队与 IM 群资格；不使用/保存/伪造本人 Token。仅在来源读取成功、模型调用许可 `BeginModel` 已提交后调用一次现有 `GenerateDrafts`。不使用 `inbox.received_at` 解释“明天”。
3. 模型输出仅是候选：拒绝模型提供的负责人ID、解析状态、UTC或不在授权消息里的来源/时间/称呼；复用现有多项草稿形状、消息来源和 Go 时间解释。生成后再次 `Read` 并验证原来源身份/范围/命令/参考时刻不变且当前权限仍有效；按当前受限范围调用 `ResolveMember` 完整称呼匹配，只生成待本人审查的候选。保存前再次受限读，防长时间核验期间离队。来源与模型/RPC调用都在数据库写事务外；候选1—5项全有效才调用上批 `CompleteDraftCollection`，固定键 `agent-trigger:tasks:<message_id>` 与原消息参考摘要同事务保存。出现错误不保存部分草稿，不创建 Task/回帖。
4. worker 遇到处理失败且仍持有租约时调用 `Release`；模型已用满两次则按 A61 `exhausted`，否则排回 queued 并写持久 `retry_after`。首次失败等30秒，连续失败翻倍，最多等1小时；`retry_failures` 上限8，只控制等待，不增加模型次数。Claim 只领取到期 queued 或过期 running，避免一条撤权通知持续挡住后面来源。等待期间 Kafka 重放只检查不可变接收事实，不清除退避/预算。数据库/续租异常使 worker 停止并记录安全错误，修复重启后按持久状态恢复；主动停机让租约到期恢复。

备选停止整个worker等待人工修复，改动少但一条暂时失败会阻塞后续通知；用户本轮选择持久退避。退避无限次但间隔有上限：已离群者在其恢复前不会生成草稿，队列仍会定时重新核权。模型最多两次预算独立；第二次调用后失败变 exhausted。026 只增加 Agent inbox 两列及索引，旧行默认可立即领取，不改 IM/User/Task 数据归属、证书或服务边界。

## 共同接口与分工

- `TriggerProcessor.Process(ctx, lease)` 处理一次来源，不自行领取/续租/释放；`NewTriggerTaskProcessor(server *Server, source *TriggerContextClient, store *TriggerInboxStore)` 构造生产处理器，可在同包测试注入生成器/来源/存储替身。处理器执行顺序为首次 Read → BeginModel → GenerateDrafts → 再次 Read/全项核验与受限负责人解析 → 最终 Read → `CompleteDraftCollection`。生成/外部RPC失败返回错误，调用方依据持久预算 Release；不自行重试模型。
- `NewTriggerWorker(store TriggerExecutionStore, processor TriggerProcessor)` 提供 `Run(ctx) error`，串行领取、续租、调用 Processor 和带退避释放；固定轮询2秒、续租10秒。`Run` 直到取消或存储/续租错误，错误不泄漏正文、证书或数据库细节。worker不从 Kafka 直接读消息，Kafka接收与模型执行的故障域分离。
- 生产入口用 `AGENT_TRIGGER_WORKER_ENABLED=true` 单独开启，默认关闭；开关开启必须完整配置现有 `AGENT_IM_TRIGGER_*` mTLS 参数，重用已经创建的草稿 Eino 生成器和 Snowflake 节点，不额外创建第二个模型客户端/节点。启动/停机由 `cmd/agent` 管理，普通 RPC 停止与 worker 停止各自有界。阶段末仍不启用云配置或执行迁移。

| 角色 | 独立文件范围 | 目的 |
| --- | --- | --- |
| 执行 A | `rpc/agent/draft_preparer.go`，新 `trigger_processor.go/test.go` | 提取共用的草稿证据核验并接无 Token 的受限来源、负责人、时间及原子保存。 |
| 执行 B | `rpc/agent/trigger_lease_claim.go`、`trigger_lease_attempt.go`、`trigger_lease_store.go`、对应旧测试及新 `trigger_retry_test.go` | 持久退避、到期领取、结果状态严格读取；不改共同迁移/协议。 |
| 执行 C | 新 `rpc/agent/trigger_worker.go/test.go`，`cmd/agent/main.go` 与新 `cmd/agent/trigger_worker.go/test.go` | 串行轮询/续租/关闭与生产进程的默认关闭接线；不改 A/B 文件。 |
| 主 agent | 026/init、共同接口/契约、ADR/计划/审查及跨层组合测试、Git整合 | 保持单一基线和完整验证；三执行agent不得改共同文件、迁移/依赖/生成代码或合并main。 |

最多九个独立小步骤按整批计算；关键新选型继续先讨论。共同文件由主 agent 修改，子 agent 只在独立 worktree 改指定文件，测试与提交/合并由主 agent 集中做。真实 MySQL/Kafka/证书/模型/容器验收留最终统一联调。
