# 阶段 6：后台触发恢复验证契约

日期：2026-10-05。基线为已实现的 A61/A63/A64；本批只验证既定行为，不更改服务边界、权限规则、数据归属、RPC 或迁移。上批 `codex/trigger-status-integration` 尚未合入 `main`，本批从它的完整提交继续。

业务目的：相同群指令可能被重复通知，worker 可能在生成中退出，发起人也可能在等待期间离群。验证系统在这些时刻仍只生成一份待审草稿，旧 worker 不会夺回已过期的租约，权限撤销后不会继续读取或保存结果。模型最多两次尝试；草稿仍须由本人逐项确认，不能由后台创建 Task 或回帖。

本批分三项相互独立的测试，全部以现有生产实现为测试对象，使用现有 Kafka/SQL/RPC 替身；不得把替身称作真实 MySQL/Kafka 或进程崩溃验收。

| 项 | 验证目标 | 执行 worktree | 允许改动 |
| --- | --- | --- | --- |
| 通知重放 | 同一持久消息的 Kafka 重放不重置 running/completed/exhausted 的事实或再次生成草稿 | `D:/zy/GoLang/go-im/.worktrees/assignee-backend` | 仅新 `rpc/agent/trigger_replay_recovery_test.go` |
| 重启恢复 | 新 worker/存储实例只在旧租约过期后领取；旧持有者不能再次花模型预算或保存草稿 | `D:/zy/GoLang/go-im/.worktrees/assignee-gateway` | 仅新 `rpc/agent/trigger_restart_recovery_test.go` |
| 权限撤销 | 原触发者失去当前团队群资格时，后台处理不得生成/保存草稿；本人状态查询也不得泄露结果 | `D:/zy/GoLang/go-im/.worktrees/assignee-ui` | 仅新 `rpc/agent/trigger_revocation_recovery_test.go` |

三个执行 agent 不改协议、生成代码、迁移、生产文件、共同文档或其他执行文件；先回报现有覆盖及拟新增断言，避免重复测试。主 agent 统一处理测试发现的生产缺陷、集中验证、Git 提交和整合。至少覆盖一条先前测试未覆盖的跨环节行为；若发现现有测试足够，应回报证据，不为数量重复写测试。

每项开始前交代目的、问题与文件；完成后列出实际测试和限制。主 agent 汇总时以整个批次最多九个小步骤计算。真实 MySQL/Kafka、mTLS、浏览器和云部署留待统一环境验收。
