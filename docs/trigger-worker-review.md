# 后台 @AI 草稿 worker：本批审查

日期：2026-10-04。本批沿 A60/A61 与用户本轮选定的 A63 持久退避，在 `codex/trigger-worker-integration` 完成。批次开始时，上批已快进到本地 main `ef78ba8`；三个独立执行分支已由主 agent 合入本批集成分支，尚未合 main、推送或部署。[共同契约](trigger-worker-contract.md)、[A63取舍](architecture-decisions.md#a63后台-worker-的前置失败调度用户选择-a2026-10-04)。

业务目的：此前群内 `@AI 整理任务` 已能可靠保存通知、受限读来源、领取租约、原子写草稿，但尚没有进程把它们连续执行。本批接通可显式开启的后台执行：通知进入 Agent inbox 后，即使浏览器关闭，worker 也可以生成**待本人审查**的草稿；不自动创建 Task 或发送群卡片。默认开关仍关闭，所以仅更新本地代码不会改变现有部署行为。

## 九个独立小步骤

| 步骤 | 做了什么、为什么、解决的问题 | 实际文件 |
| --- | --- | --- |
| 1 | 记录 A63 与三条子任务共同接口、状态和文件边界，避免退避、处理器和进程接线各自假设不同。 | `docs/architecture-decisions.md`、`project-plan.md`、`trigger-worker-contract.md`、`rpc/agent/trigger_worker_contract.go`；修正 `trigger-result-contract.md` 对既有事务方法的描述。 |
| 2 | 给 inbox 加 `retry_after`/`retry_failures` 及领取索引，并核对新库与 023→026 的定义，旧行默认可领取。 | `deploy/mysql/init.sql`、`migrations/026_agent_trigger_retry.sql`、`rpc/agent/trigger_inbox_flow_test.go`。 |
| 3 | 从既有本人草稿流程提取纯证据核验，仍由本人流程使用原 Bearer 负责人解析；后台复用同一时间、来源和候选规则。 | `rpc/agent/draft_preparer.go`。 |
| 4 | 后台处理器先读取持久来源/当前资格，先记一次模型预算，再用既有 Eino 生成器生成一次；三次受限读取核对不变来源、候选证据及最终权限，1—5项全有效才原子保存。 | `rpc/agent/trigger_processor.go`、`trigger_processor_test.go`。 |
| 5 | 领取只选到期 queued 或过期 running，失败时按数据库 UTC 持久等待30秒起、翻倍至1小时，失败计数封顶8且不重置两次模型预算；旧结果测试同步12列。 | `rpc/agent/trigger_lease_claim.go`、`trigger_lease_attempt.go`、`trigger_lease_store.go`、对应四份 `trigger_lease_*_test.go`、`trigger_retry_test.go`、`trigger_result_flow_test.go`、`trigger_result_store_test.go`。 |
| 6 | Worker 串行领取、空队列等待、10秒续租、失败释放；失去租约即取消处理并停下，提交成功与续租失主竞态不误释放，停机有界。 | `rpc/agent/trigger_worker.go`、`trigger_worker_test.go`。 |
| 7 | Agent 进程默认不开 worker；只有显式开关和完整专用 mTLS 配置才构造它，复用原 Server 的生成器与 ID 节点。普通 RPC 和 Kafka inbox 不依赖 worker 健康，停机先取消再关专用客户端。 | `cmd/agent/main.go`、`trigger_worker.go`、`trigger_worker_test.go`。 |
| 8 | 主 agent 提交并无冲突合入三条子分支，补结果测试夹具，执行全仓、重复并发场景与 Linux 构建。 | 第5步所列旧测试夹具与本批所有代码文件。 |
| 9 | 将实际结果、下一步与全部文件写入计划、架构记录、worktree记录及本页，保留未验证边界。 | `docs/project-plan.md`、`architecture-decisions.md`、`worktree-collaboration-plan.md`、本文件。 |

关键调用链：IM 保存消息/Outbox → Kafka 通知 → Agent inbox → worker 到期领取租约 → IM 专用 mTLS RPC 按保存的消息 ID 重查当前团队/群资格和原消息时刻 → `BeginModel` 持久记预算 → Eino 生成一次 → 再次核对消息证据、负责人称呼及当前权限 → 同一事务保存 Agent run、各待审草稿和 inbox 的 `completed + result_run_id`。任何模型或 RPC 调用都不在该数据库事务内；失败只由 worker 在仍持有租约时写退避或耗尽状态。本人确认 Task/回帖仍走既有显式流程。

## 全部实际修改文件（28）

| 用途 | 文件定位 |
| --- | --- |
| 数据库 | [init.sql](../deploy/mysql/init.sql)、[026_agent_trigger_retry.sql](../deploy/mysql/migrations/026_agent_trigger_retry.sql) |
| 生产代码 | [main.go](../cmd/agent/main.go)、[cmd/agent/trigger_worker.go](../cmd/agent/trigger_worker.go)、[draft_preparer.go](../rpc/agent/draft_preparer.go)、[trigger_lease_attempt.go](../rpc/agent/trigger_lease_attempt.go)、[trigger_lease_claim.go](../rpc/agent/trigger_lease_claim.go)、[trigger_lease_store.go](../rpc/agent/trigger_lease_store.go)、[trigger_processor.go](../rpc/agent/trigger_processor.go)、[trigger_worker.go](../rpc/agent/trigger_worker.go)、[trigger_worker_contract.go](../rpc/agent/trigger_worker_contract.go) |
| 测试 | [cmd/agent/trigger_worker_test.go](../cmd/agent/trigger_worker_test.go)、[trigger_inbox_flow_test.go](../rpc/agent/trigger_inbox_flow_test.go)、[trigger_lease_attempt_test.go](../rpc/agent/trigger_lease_attempt_test.go)、[trigger_lease_claim_test.go](../rpc/agent/trigger_lease_claim_test.go)、[trigger_lease_flow_test.go](../rpc/agent/trigger_lease_flow_test.go)、[trigger_lease_store_test.go](../rpc/agent/trigger_lease_store_test.go)、[trigger_processor_test.go](../rpc/agent/trigger_processor_test.go)、[trigger_result_flow_test.go](../rpc/agent/trigger_result_flow_test.go)、[trigger_result_store_test.go](../rpc/agent/trigger_result_store_test.go)、[trigger_retry_test.go](../rpc/agent/trigger_retry_test.go)、[trigger_worker_test.go](../rpc/agent/trigger_worker_test.go) |
| 设计与审查 | [architecture-decisions.md](architecture-decisions.md)、[project-plan.md](project-plan.md)、[trigger-result-contract.md](trigger-result-contract.md)、[trigger-worker-contract.md](trigger-worker-contract.md)、[trigger-worker-review.md](trigger-worker-review.md)、[worktree-collaboration-plan.md](worktree-collaboration-plan.md) |

## 验证与限制

- 三子分支整合后的 `go test ./...` 全仓通过；`go test ./rpc/agent ./cmd/agent -run 'TestTriggerWorker|TestAgentTriggerWorker' -count=5` 通过；`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` 通过；`git diff --check` 通过。
- 后台处理器的替身测试覆盖单项/五项、相对时间原消息时刻、来源/资格变化、候选证据不在原模型上下文、成员解析异常、模型或保存失败不自行重试，以及提交已成功后取消仍视为成功。worker 测试覆盖领取/续租/释放、失租/完成竞态、取消与有界退出；进程测试覆盖默认关闭、配置顺序与普通 RPC 隔离。SQL 替身只能验证语句、参数、事务意图与顺序。
- `go test -race` 在本机未运行成功：当前 `CGO_ENABLED=0` 且没有 gcc。没有因此改项目工具链；并发测试重复运行并不等同竞态检测。
- 未执行 022—026 迁移，未在真实 MySQL/Kafka 或容器里运行，未使用生产 mTLS 证书、真实方舟模型、浏览器或云服务器。未验证真实数据库的锁竞争、时钟和索引效果。开关仍默认关闭，尚无本人查看后台 run/草稿的入口或端到端崩溃恢复验收；阶段6仍未全部完成。
