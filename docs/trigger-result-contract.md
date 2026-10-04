# A61 后台草稿结果事务契约

日期：2026-10-04。范围仅为 Agent MySQL 中持久化后台生成结果；本批不启动 worker 或模型，不创建 Task 或自动群回帖。此为用户已确认 A61 持久状态与租约方案的实现分解。

## 状态与数据

- inbox 保持既有 queued/running/exhausted，并增加终态 `completed` 和可空 `result_run_id`。只有 completed 可带正数 run ID；completed 必须没有租约 token/期限，`model_started=0` 且已消耗 1—2 次模型预算。其余状态的 run ID 均为空。重复 Kafka 通知只核对不可变来源事实，不重置 completed 结果。
- `agent_runs` 与 `agent_task_drafts` 仍归 Agent；一个触发来源生成一个 collection run，最多五项，逐项等待本人审查。运行的 request key 必须等于 `agent-trigger:tasks:<message_id>`，run scope 来自受限 IM 来源读取，不接受模型提供的 actor/team/group。fingerprint 使用同一来源指令及原消息参考时间；本批保存接口接收已验证的 scope、草稿、固定 key/fingerprint，不自称完成来源读或生成。
- 025 在 inbox 增加 `result_run_id BIGINT NULL` 与唯一索引；旧行均为 NULL。新库 init.sql 与 023→024→025 的表定义应一致。不开外键：历史表没有统一外键约束，但提交时必须同事务插入 run/项、更新 inbox；唯一索引防一份 run 被两条来源关联。

## 调用与失败边界

1. worker 先持有 live lease，且 `BeginModel` 已成功提交。模型/RPC、来源核对及负责人/时间解释都在 SQL 事务外完成；只有已核验的草稿进入保存方法。
2. 后台保存方法预检 run ID、scope、draft 数量/每项完整元数据、fixed key/fingerprint；使用与 `withTriggerLease` 同样的有界事务及锁/数据库 UTC 校验，但完成写在专用事务方法中实现，因为通用方法的末尾 live 检查会与 completed 冲突。草稿 run/项与 `completed + result_run_id` 更新在**同一事务**。更新行数必须为 1；顺序为：锁 live → 草稿 SQL → 再检查 live → 条件更新为 completed → commit。
3. 任一步失败回滚全部写；失去租约返回 `ErrTriggerLeaseLost`，数据库故障对外屏蔽细节为 Unavailable。提交不确定时，不能重新调用模型或换 key 插入；后续根据保存的 inbox 状态和固定 key 查证。已完成不再可领取，通知重放不重置。
4. 原同步多项保存接口保持独立事务和既有冲突回放行为；后台接口不能直接调用它并在另一个事务标记完成。

备选只靠 request key 扫描 run 不记 inbox 结果关联，会使执行状态和本人入口难以准确对应；备选先独立保存 run 再更新 inbox 存在崩溃半完成窗口。采用本事务契约，代价是 025 增量迁移和短行锁。`result_run_id` 不暴露给其他用户作为权限凭证，后续本人读取仍检查当前团队/群资格。

## 并行文件边界

- 执行 A（草稿 SQL）：只改 `rpc/agent/draft_collection_store.go` 和新增 `rpc/agent/trigger_result_draft_test.go`。提取现有草稿预检/`*gorm.DB` 插入辅助函数供同步及后台共用；同步测试保持原样，绝不另开后台事务。
- 执行 B（状态/保存）：只改 `rpc/agent/trigger_lease_store.go`、`rpc/agent/trigger_inbox_store.go`、`rpc/agent/trigger_lease_contract.go` 和新增 `rpc/agent/trigger_result_store.go`、`rpc/agent/trigger_result_store_test.go`。可调用 A 约定的 `prepareWaitingDraftCollection` 与 `insertWaitingDraftCollection`，若签名需调整先报告主 agent。不要编辑 A 的文件。
- 执行 C（跨流程/迁移验证）：只新增 `rpc/agent/trigger_result_flow_test.go` 和 `rpc/agent/trigger_result_schema_test.go`。根据本契约写 SQL 替身组合测试：成功一次事务、项失败/过期/旧 token 回滚、completed 重放不重置、025 与 init 相符。不得触碰生产代码或已有测试。
- 主 agent 拥有此契约、init/025、共同文档、分支合并和全量验证；各工作树从同一共同提交启动。生产代码在各自分支完成前允许定向测试暂不通过，最终合并后必须通过。

共同 Go 接口：A 提供 `prepareWaitingDraftCollection(scope draftRunScope, drafts []taskDraft) ([]taskDraft, error)` 与 `insertWaitingDraftCollection(tx *gorm.DB, runID int64, scope draftRunScope, items []taskDraft, requestKey, fingerprint string) error`；B 提供 `(*TriggerInboxStore).CompleteDraftCollection(ctx context.Context, lease TriggerLease, runID int64, scope draftRunScope, drafts []taskDraft, requestKey, fingerprint string) (int64, error)`。B 内部先校验固定 key 再调用 A helper。提交成功返回 runID，错误返回 0；没有“已有结果则假装本次成功”的不安全分支。
