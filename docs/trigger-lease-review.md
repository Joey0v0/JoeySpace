# Agent 触发租约与模型预算：九步审查

日期：2026-10-04。沿用户已选 A61；上批 a44920c 已快进合本地 main，本轮整合分支 codex/trigger-lease-integration。共同 28a98e3，领取 7765ded、预算 2c160a4、重放 1d7a05d，三个 worktree 保留。没有删除已有文件、push、执行迁移或同步云端。

## 九步实际范围

| 步骤 | 做了什么、解决什么 | 边界 |
| --- | --- | --- |
| 1 | root 统一状态/租约/预算 API、严格行校验、事务防护和024/init | inbox只扩四列；不增加后台用户权限或草稿完成状态 |
| 2 | queued/数据库已过期running短事务领取，SKIP LOCKED、新随机token | 领取不花模型预算；事务成功才能交出可用租约 |
| 3 | 当前token续租、两次已耗过期记录退休、异常测试 | DB时间判有效；不缩短期限，旧/过期持有者不能操作 |
| 4 | BeginModel提交前增加持久预算，同租约重复不给调用许可 | 提交成功true才能调用；提交不确定false，预算保守保留 |
| 5 | 失败释放：未耗/首尝试回queued，两次已耗exhausted | 清持有者，不减少次数，不改通知事实/时间 |
| 6 | 通知重放兼容running/exhausted且只读事实 | 不重置队列、期限、预算，原A62异常策略不变 |
| 7 | 重放/预算/取消/不确定提交与错误保护测试 | 只已知状态；当前资格仍须后续A60检查 |
| 8 | root同事务防护、跨存储重构/预算/生产publisher-consumer组合、迁移一致性 | 写入中失效回滚；不把SQL替身当真实数据库并发 |
| 9 | root集中检查、完整文件定位与阶段记录 | 最终结果见下；不标群内后台生成或阶段6全部完成 |

## 调用链与含义

已接进程的通知消费→Agent持久inbox→同步Kafka确认。新执行存储可领取来源ID→数据库授予内部token/期限→可续租→调用模型前持久记次数→失败后释放或预算耗尽。当前没有把后半段挂进运行中的worker，也没有模型调用/草稿生成；方法与恢复机制已实现，不能说后台功能已经启动。

后续worker取得租约后，先Agent→IM受限mTLS读取原触发范围并核对当前用户资格，再通过受限负责人解析/模型生成，经同一受保护事务保存草稿与执行结果。模型次数只限自动生成，Task确认与卡片回帖仍本人逐项显式；相对日期参考原消息服务器时间，不能用inbox.received_at或worker时间。

事务防护在同一锁内SQL前后检查当前token和DB有效期限：开始已失效不调用写入callback，写入途中失效回滚全部SQL。未来不能先调用旧saveWaitingDraftCollection的独立事务，再单独检查或标完成。当前测试使用真实运行INSERT作为短SQL示例，未实现完整运行/草稿/队列结果的原子保存。

30秒租期为内部常量；后续worker必须按既定机制续租，失败取消外部工作。随机256位token每次新领取更换，只有内部使用，不能暴露为用户接口或日志。MySQL时间是期限依据，Go设备时钟不裁定有效性。GREATEST续租保证期限增加，避免同值UPDATE返回0误判；这是既定A61内数据库实施细节。

## 验证结果与限制

业务整合613b441：最终 `go test ./... -count=1` 全部通过，Linux amd64 Agent 编译通过，临时产物不进仓库。取消/回滚三个场景连续10次通过；仅测试修正后未重复Linux构建。新增33个测试函数：领取10、预算9、重放5、root共同防护7、跨存储恢复1、既有publisher/consumer的新状态组合1；另更新023→024与初始化一致性旧测试。

初次包检查发现root组合测试把整数model_started当布尔，已仅修断言。再次检查发现两处新取消测试过早核对db/sql异步Rollback，保留回滚期望并增加有界等待；首次全量又发现旧Inbox超时测试同类问题，统一本文件等待helper后通过10次定向及最终全量，生产逻辑不因此修改。测试修正a40fa04/e7eddff/8addc78均在原允许文件，root保存/整合。

SQL为sqlmock，经真实GORM事务和生产存储方法；通知组合用已有真实publisher JSON/Key及consumer，但Kafka为接口替身。未验证真实MySQL行锁竞争/SKIP LOCKED、时钟/崩溃/迁移或Kafka消费组再平衡；未开启Outbox或Agent接收开关，没有真实模型/浏览器/容器/云部署。024未执行；旧库将来需按023→024升级，不改历史023。

## 全部实际修改文件（18个）

| 文件 | 审查目的 |
| --- | --- |
| [rpc/agent/trigger_lease_contract.go](../rpc/agent/trigger_lease_contract.go) | 新状态、预算、租约数据与API |
| [rpc/agent/trigger_lease_store.go](../rpc/agent/trigger_lease_store.go) | 严格行校验、数据库有效锁、短事务防护 |
| [rpc/agent/trigger_lease_store_test.go](../rpc/agent/trigger_lease_store_test.go) | 状态矩阵、NULL/坏行、同事务过期回滚 |
| [rpc/agent/trigger_lease_claim.go](../rpc/agent/trigger_lease_claim.go) | 领取、随机token、过期退休、续租 |
| [rpc/agent/trigger_lease_claim_test.go](../rpc/agent/trigger_lease_claim_test.go) | 领取/续租与提交故障保护 |
| [rpc/agent/trigger_lease_attempt.go](../rpc/agent/trigger_lease_attempt.go) | 模型次数许可与失败释放 |
| [rpc/agent/trigger_lease_attempt_test.go](../rpc/agent/trigger_lease_attempt_test.go) | 两次预算、重放、故障与取消 |
| [rpc/agent/trigger_inbox_store.go](../rpc/agent/trigger_inbox_store.go) | 已执行状态通知重放兼容 |
| [rpc/agent/trigger_inbox_store_test.go](../rpc/agent/trigger_inbox_store_test.go) | 重放不写状态/预算/时间 |
| [rpc/agent/trigger_lease_flow_test.go](../rpc/agent/trigger_lease_flow_test.go) | 重构存储后两次预算及旧token失效组合 |
| [rpc/agent/trigger_inbox_flow_test.go](../rpc/agent/trigger_inbox_flow_test.go) | publisher→consumer新状态重放及迁移一致性 |
| [deploy/mysql/migrations/024_agent_trigger_lease.sql](../deploy/mysql/migrations/024_agent_trigger_lease.sql) | 既有队列增量四列/恢复索引，未执行 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | 新库同定义 |
| [docs/trigger-lease-contract.md](trigger-lease-contract.md) | 共同契约与分工 |
| [docs/trigger-lease-review.md](trigger-lease-review.md) | 九步、调用链、完整文件和限制 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A61备选/理由/代价/既定确认状态 |
| [docs/project-plan.md](project-plan.md) | 本地已验证成果与下一步 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | 三分支整合与验证记录 |
