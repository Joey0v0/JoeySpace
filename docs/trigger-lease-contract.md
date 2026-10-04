# Agent 触发租约与模型预算共同契约

2026-10-04；沿用户已确认 A61，不改变 Agent 数据归属或后台权限。现有 inbox 扩展四列，不另建状态表；023 原样保留，旧库先 023 再 024，新库直接 init。迁移未执行。

## 状态与租约

- queued：没有持有者；model_attempts 为 0 或 1，lease 两列 NULL，model_started=0。
- running：持有内部随机 256 位小写十六进制 token，lease_until 非空；attempts 为 0—2。model_started=1 时 attempts 必须至少 1。
- exhausted：两次模型预算已经使用，没有持有者；attempts=2，两列 NULL，model_started=0。不宣称生成/Task成功。

领取短事务以数据库 UTC_TIMESTAMP(6) 判断过期，按来源 ID 取一条 queued 或已过期 running，FOR UPDATE SKIP LOCKED。每次新领取生成新 token，30 秒租期；过期且已用两次先退休为 exhausted，本次返回 nil，下次轮询继续。领取/续租/来源授权失败不增加模型次数。相同来源 Kafka 重放只核对 immutable facts 和已知状态，不重置状态、接收时间或预算；完整执行字段由执行存储严格核验。

## 共同 Go API

rpc/agent/trigger_lease_contract.go 的 TriggerExecutionStore 由 TriggerInboxStore 实现：Claim、Renew、BeginModel、Release。每次 SQL 最多 3 秒，数据库故障安全报告，不泄露 SQL/正文/token；nil context/无效 ID/token 拒绝，调用者取消优先。

BeginModel 在同一短事务核对当前有效持有者、model_started=0、预算<2，增加次数并设置 started=1，提交成功才返回 true。重复同租约返回 false，不能再调模型；提交不确定返回错误，保守消耗预算，不假定“没有调用就不计”。失败后 Release 清持有者并回 queued；第二次已耗则 exhausted。旧持有者、过期者不能续租、记次数、释放或写草稿，即使尚未被另一执行者领取。

root 提供共同严格行校验、有效租约锁与 withTriggerLease 私有事务防护。后续草稿保存必须在此同一 SQL 事务内写入并完成最后租约核对；禁止先调用原自有事务的 saveWaitingDraftCollection 再单独标成功。callback 只能短 SQL，不能模型/RPC。当前批次没有草稿完成状态/后台 worker，不开放该能力。

## 分工与九步

1 root：共同类型、严格行校验/事务 helper、024/init、契约。
2—3 backend：Claim/过期恢复，Renew/旧持有者保护；只 rpc/agent/trigger_lease_claim.go 与同名 _test.go。
4—5 gateway：BeginModel/最多两次，Release/失败恢复；只 rpc/agent/trigger_lease_attempt.go 与同名 _test.go。
6—7 ui：通知重放兼容已运行/耗尽，测试重复通知不重置；只 rpc/agent/trigger_inbox_store.go、trigger_inbox_store_test.go。
8 root：共同防护测试、SQL/消费组合与迁移一致性。
9 root：集中检查、审查文档和当前进度。

子 agent 不运行测试/build、不提交/合并/push、不越界文件。root 统一集中验证/Git，不启用开关/数据库迁移/模型/云部署。租期 30 秒是内部实施常量，后续执行器必须续租并在失败时取消外部工作；不把 token 当用户身份，生成仍须 A60 当前范围核对，日期仍源消息时间，Task/回帖仍本人操作。
