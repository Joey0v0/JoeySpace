# 阶段 6 后台恢复组合验证：本批审查

日期：2026-10-05。用户要求继续推进阶段6；本批沿既定 A61/A63/A64 验证，不引入新服务、通信方式、权限规则或迁移。上批 `codex/trigger-status-integration` 尚未合入 `main`，本批从其提交另建 `codex/trigger-recovery-integration`，三个执行分支已合入本批本地集成分支；`main` 仍是 `9c45e13`，未推送或部署。[共同契约](trigger-recovery-contract.md)。

业务目的：群内 `@AI 整理任务` 的通知可能重放，worker 可能在模型调用后退出，发起人也可能离开群。需要确认这些边界不会重复保存草稿、重置模型次数或绕过当前群资格。三组测试将现有生产组件接在一起，以 SQL、Kafka、IM/User 和模型替身控制故障；它们不是实际杀进程或真实中间件验收。

| 小步骤 | 做了什么、为什么、解决的问题 | 实际文件 |
| --- | --- | --- |
| 1. 固定范围 | 说明三条恢复路径、唯一测试文件边界和替身限制，使三个 worktree 从同一基线协作。 | [trigger-recovery-contract.md](trigger-recovery-contract.md) |
| 2. 完成通知重放 | 生产结果存储原子保存两项草稿；Publisher、Consumer 和 Inbox 在 Kafka ACK 首次失败、重建后重复通知时只核对完成事实。再通过真实 Agent gRPC 查询原 run 和两项待审草稿，防止重放把已完成的消息变成新草稿。 | [trigger_replay_recovery_test.go](../rpc/agent/trigger_replay_recovery_test.go) |
| 3. 旧租约恢复 | 真实 Worker 和 InboxStore 串起来：首个 worker 已记一次模型预算后停止，新实例在数据库选择过期租约后换 token 继续，旧 token 不得记账、释放或保存；第二次失败进入 exhausted。用通道控制顺序，避免依赖真实等待30秒。 | [trigger_restart_recovery_test.go](../rpc/agent/trigger_restart_recovery_test.go) |
| 4. 离群撤权 | 真实 Worker/Processor/Store 与本机 mTLS 来源 RPC 验证首次读取前和模型调用中撤权；不写草稿，失败按持久退避释放。随后真实 Agent TCP 状态 RPC 拒绝离群者；用户 Token 失效则先拒绝，不把 Token 过期当成后台 worker 的授权信号。 | [trigger_revocation_recovery_test.go](../rpc/agent/trigger_revocation_recovery_test.go) |
| 5. 集中验证和记录 | 主 agent 审查、合入三分支，运行定向测试五次与全仓 Go 回归；如实更新阶段进度和协作记录。 | [project-plan.md](project-plan.md)、[worktree-collaboration-plan.md](worktree-collaboration-plan.md)、本文件 |

调用链：IM 已保存消息经 Outbox 发布 → Agent Consumer 持久 Inbox → Worker 领取租约、向 IM mTLS RPC 复核原消息及当前资格 → 记模型预算 → 生成并再次复核 → 原子保存草稿。本人从原消息点状态入口时，Gateway → Agent RPC → User 当前身份 → IM 当前团队群资格 → Inbox 状态；完成态再读草稿集合。测试分别卡在重放、租约交接和撤权位置，确认跨环节的状态仍一致。

全部实际修改文件为上表的三份 Go 测试，以及四份文档：本文件、[共同契约](trigger-recovery-contract.md)、[阶段计划](project-plan.md)、[worktree 协作记录](worktree-collaboration-plan.md)。没有修改生产代码、协议、迁移或依赖。

验证结果：三组新增测试定向通过，随后 `go test ./rpc/agent -run '^TestTrigger(CompletedReplayAndACKRetryRecoverOriginalRunThroughOwnerRPC|RestartRecoveryWorkerKeepsChargedBudgetAndFencesOldOwner|RevocationRecoveryWorkerAndStatusShareCurrentAuthorization)$' -count=5` 通过；`go test ./...` 全仓通过；`git diff --check` 通过。由于只增加 Go 测试，未重复页面测试和 Linux 编译。SQL 仍是 sqlmock；Kafka、IM/User 和模型为替身，mTLS 仅使用本机测试证书；没有真实 MySQL 的数据库时钟/锁并发、真实 Kafka、操作系统崩溃、浏览器、方舟模型或云端验证。022—026 迁移仍未执行，阶段6不能据此标为最终完成。
