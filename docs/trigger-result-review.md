# 后台任务草稿结果原子保存：本批审查

日期：2026-10-04。范围：沿用户已确认的 A61 持久状态/租约方案，先完成 Agent 数据库中的结果提交边界。main 已在批次开始时本地快进到上批 `79ab797`；本批在 `codex/trigger-result-integration`，三个执行分支已合入本批，尚未合 main、推送或部署。[共同契约](trigger-result-contract.md)、[架构取舍](architecture-decisions.md#a61-后台草稿与执行结果原子保存2026-10-04)。

业务意义：群里 `@AI 整理任务` 的通知已有可靠排队、受限来源读取和租约，但此前后台尚不能持久化“生成了哪一轮草稿”。旧同步多项保存会自己开启事务，若先保存它再单独更新 inbox，崩溃或租约过期可留下半完成。本批让两个结果同事务提交，方便下批 worker 安全接线；尚未运行 worker，因此群内指令现在**不会自动生成草稿**。

## 九个小步骤与调用链

| 步骤 | 做了什么、为什么、解决的问题 | 实际文件 |
| --- | --- | --- |
| 1 | 固定结果契约：completed 终态、正 run ID 关联、单事务及失败语义；避免各 worktree 对状态/接口理解不同。 | `docs/trigger-result-contract.md` |
| 2 | 增量 025 给 inbox 增可空、唯一结果关联；同步新库定义且检验 023→024→025，一直保留旧行 NULL。 | `deploy/mysql/init.sql`、`deploy/mysql/migrations/025_agent_trigger_result.sql`、`rpc/agent/trigger_inbox_flow_test.go` |
| 3 | 抽出整批草稿预检/规范化，任一非法项在 SQL 前拒绝；后台和原同步入口共用相同字段规则。 | `rpc/agent/draft_collection_store.go`、`rpc/agent/trigger_result_draft_test.go` |
| 4 | 抽出调用者事务内的 run/1—5 项插入，逐条要求恰好一行；原同步入口继续自己开事务并保留 1062 请求键赢家核对。 | 同上 |
| 5 | inbox 严格读 `result_run_id`：completed 必须有正结果、无租约且模型预算已消耗；其他状态不许带结果。重复通知仅查不可变来源，不清 completed。 | `rpc/agent/trigger_lease_contract.go`、`trigger_lease_store.go`、`trigger_inbox_store.go`、`trigger_result_store_test.go`（后三个均在 `rpc/agent`） |
| 6 | `CompleteDraftCollection` 校验固定来源键和已核验草稿，锁当前租约且确认 BeginModel 记账，事务内插入 run/项、再次以数据库时间核对租约、条件更新 completed+run ID 后提交；旧持有者与失败路径回滚。 | `rpc/agent/trigger_result_store.go`、`trigger_result_store_test.go` |
| 7 | 旧租约与通知测试的 SQL 行补新结果列，维持旧状态 NULL；修正一次遗漏的重复行构造。 | `rpc/agent/trigger_inbox_store_test.go`、`trigger_inbox_flow_test.go`、`trigger_lease_store_test.go`、`trigger_lease_claim_test.go`、`trigger_lease_attempt_test.go` |
| 8 | 用生产 store/consumer 搭配 SQL/Kafka 替身验证两项结果、原九个时间依据、1/2 次预算、项失败/租约失效/写完成失败/提交不确定、completed 通知重放和 schema。 | `rpc/agent/trigger_result_flow_test.go`、`trigger_result_schema_test.go` |
| 9 | 主 agent 合并三分支，跑定向/Agent 全包/全仓回归及 Linux 编译，更新决策、计划和本审查记录。 | `docs/architecture-decisions.md`、`project-plan.md`、`worktree-collaboration-plan.md`、本文件 |

关键调用链：未来 worker 在事务**外**完成受限来源及当前权限读取、Eino 生成、负责人/时间/草稿证据核验，并先调用 `BeginModel` 记一次模型预算；本批可用的持久化边界是 `TriggerInboxStore.CompleteDraftCollection` → 锁 live inbox → 调用共用草稿 SQL 写 `agent_runs`/`agent_task_drafts` → 再查 live inbox → 条件写 completed/result_run_id → **同一事务提交**。失败返回零 run ID，数据库内部错误对外屏蔽；提交不确定不代表允许再次调用模型。本人确认 Task 与机器人卡片仍沿原来的逐项显式操作。

## 全部实际改动文件（21）

| 用途 | 文件定位 |
| --- | --- |
| 新库与增量 | [init.sql](../deploy/mysql/init.sql)、[025_agent_trigger_result.sql](../deploy/mysql/migrations/025_agent_trigger_result.sql) |
| Agent 生产代码 | [draft_collection_store.go](../rpc/agent/draft_collection_store.go)、[trigger_inbox_store.go](../rpc/agent/trigger_inbox_store.go)、[trigger_lease_contract.go](../rpc/agent/trigger_lease_contract.go)、[trigger_lease_store.go](../rpc/agent/trigger_lease_store.go)、[trigger_result_store.go](../rpc/agent/trigger_result_store.go) |
| Agent 测试 | [trigger_inbox_flow_test.go](../rpc/agent/trigger_inbox_flow_test.go)、[trigger_inbox_store_test.go](../rpc/agent/trigger_inbox_store_test.go)、[trigger_lease_attempt_test.go](../rpc/agent/trigger_lease_attempt_test.go)、[trigger_lease_claim_test.go](../rpc/agent/trigger_lease_claim_test.go)、[trigger_lease_store_test.go](../rpc/agent/trigger_lease_store_test.go)、[trigger_result_draft_test.go](../rpc/agent/trigger_result_draft_test.go)、[trigger_result_flow_test.go](../rpc/agent/trigger_result_flow_test.go)、[trigger_result_schema_test.go](../rpc/agent/trigger_result_schema_test.go)、[trigger_result_store_test.go](../rpc/agent/trigger_result_store_test.go) |
| 契约与审查 | [architecture-decisions.md](architecture-decisions.md)、[project-plan.md](project-plan.md)、[trigger-result-contract.md](trigger-result-contract.md)、[trigger-result-review.md](trigger-result-review.md)、[worktree-collaboration-plan.md](worktree-collaboration-plan.md) |

## 验证与界限

- `go test ./rpc/agent -run TestTriggerInboxMigrationMatchesInitialization -count=1`：共同 schema 起点通过；`go test ./rpc/agent -run 'Test(PrepareWaitingDraftCollection|InsertWaitingDraftCollection|WaitingDraftCollection)' -count=1`：草稿分支通过。
- 三分支整合后 `go test ./rpc/agent -count=1` 首次由旧 sqlmock 重复行少一列触发 panic；只补测试行列，重跑通过。新增草稿 helper 6 组、完成存储 9 个函数、跨流程 7 个函数及 schema 2 个函数。
- 最终 `go test ./... -count=1` 全仓通过；`GOOS=linux GOARCH=amd64 go build -o bin/im-agent-linux ./cmd/agent` 成功。`bin/` 被忽略，编译产物没有纳入提交；`git diff --check` 通过。
- SQL 替身证明调用顺序、参数及回滚意图，不证明真实 MySQL 的时钟、并发锁、唯一索引、迁移执行或进程崩溃后的行为。未启动真实 Kafka/容器/浏览器，未配置生产 mTLS 证书，未请求真实模型或腾讯云部署；025 以及先前 022—024 仍待最终实际环境迁移。工作只完成存储边界，未把租约、受限来源、Eino、负责人和页面串成运行中的后台 worker，也没有本人发现后台 run 的入口。

本批架构选择与备选、成本已在[架构记录](architecture-decisions.md#a61-后台草稿与执行结果原子保存2026-10-04)登记；若下批需要改变权限、数据归属或重试策略，先与用户讨论。
