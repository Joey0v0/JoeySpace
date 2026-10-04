# 多项草稿逐项确认共同契约

2026-10-04。沿已确认 A37/A41/A50/A54，接每项同步确认、持久冻结、Task 结果保存及本人显式重试。上轮逐项编辑 4700612 已合本地 main。本批不接跳过、逐项回帖或页面，不新增迁移/依赖/权限角色/后台执行。真实环境验收留后续。

## 身份与协议

新增 ConfirmTaskDraftItem 返回 GetTaskDraftItemResponse。run_id 正；optional item_index 必有且0..4；expected_revision正；expected_title/description 为已读取的完整规范文字，沿200/2000 Unicode限制，说明可空；optional expected_assignee_id、expected_due_at_unix_ms 均必有，0明确未指派/无截止，值非负且时间<=253402300799999；expected_deadline_resolution 必有且合法。needs_input 可以作为审查输入但仍拒绝确认，不能用字段缺失绕过歧义。每次User确定本人，完整collection读取、IM当前群资格核对，目标不存在404；single拒绝409。waiting时正负责人复用User成员检查；creating/succeeded重放保持冻结负责人，仍重新核对本人群资格，Task继续自身权限/幂等检查。无模型重跑或时间重新解释。

复用 ConfigureDraftConfirmation、ConfigureDraftAccess 与原 TaskClient；18秒 Agent 总预算、12秒创建阶段、5秒Task子调用，Gateway路由20秒，旧入口不改。确认不调用旧run级replier：新集合回复状态暂保持disabled或not_started，空reply_msg_id，不能声称群里已经回帖；A55专用逐项回帖后续实施。

## 项状态与固定键

collection 项状态唯一权威在 agent_task_drafts；agent_runs.status 保留waiting集合头，不用任一项成功替其他项宣布成功。读取允许混合waiting_confirmation/creating/succeeded，仍完整校验mode/count/连续索引/范围/每项规范内容、依据与正revision。

- waiting：Task键空、TaskID0。
- creating：键必须精确 `agent-task-{run_id}-{item_index}`，TaskID0。
- succeeded：同一固定键、正TaskID。

未知状态/不一致键或ID均存储损坏Unavailable；本批不接skipped。原single键agent-task-{run_id}-0不改。其他项冻结/成功不阻挡当前waiting项编辑；冻结目标编辑409。冻结、结果保存不递增内容版本。不自动汇总整轮状态。

## 事务与网络

授权/审查后短事务用原完整collection SELECT FOR UPDATE，比较范围/项数与完整目标项，不比较其他项内容。必须核对版本、文字、已审查负责人及时间/处理状态，歧义保护复用现有方法。waiting仅更新该草稿status和task_request_key；WHERE run_id=? AND item_index=? AND revision=? AND status='waiting_confirmation'，受影响恰好1否则回滚。SQL参数固定：status='creating'，key，run,index,revision。不得更新agent_runs状态。已有creating/succeeded同键同内容复用；过时或变化Aborted。

事务提交后才发Task CreateTask：原Bearer，metadata idempotency-key固定项键，原team/title/description/assignee/due/source，来源为空时source_group_id0，非空用原group。Task超时/报错/空响应不解冻；当前请求返回错误，本人GET检查并显式POST重试同一项。成功TaskID须正；第二短事务再次完整锁读，范围/版本/草稿/键必须匹配，creating仅更新该项status='succeeded',task_id，WHERE run/index/revision/status='creating'/task_request_key，恰好1；已succeeded同ID幂等，不同ID拒绝。结果保存失败不报告成功，仍可用原键恢复。事务内禁止跨服务调用。短集合锁可能串行写，资格与MySQL不是跨服务原子保证。

## HTTP

POST `/api/v1/agent/runs/:run_id/drafts/:item_index/confirm`。正文必填 expected_title、expected_description、expected_revision(规范正十进制字符串)、expected_assignee_id(规范非负十进制字符串)、expected_due_at_unix_ms(JSON整数)、expected_deadline_resolution(字符串)。拒绝null/未知字段/第二JSON，已读取文字精确转发，不trim修改审查快照。

成功须run正确/范围正/count1..5/显式index正确且<count，完整合法succeeded项、正TaskID；revision等于审查版本，文字/负责人/截止/处理状态与提交完全一致。响应ID/revision仍字符串。不确定或坏响应502，不能补齐成功。Aborted/FailedPrecondition409，AlreadyExists任务键冲突409；沿既有脱敏RPC错误映射。GET集合/项允许三种合法状态与对应ID；编辑成功检查仍必须waiting/task0，不能因共享读取校验放宽而误报编辑成功。

## 八步分工

主agent契约/协议1步；后端状态校验与冻结、创建与结果保存、混合状态兼容3步；Gateway确认入口/响应2步；第三agent实际HTTP/TCP gRPC组合1步；根审查回归1步。后端只rpc/agent非生成collection相关实现/测试及必要confirm接线；Gateway只api新collection confirm实现/测试、集合validator及编辑成功保护、main路由；第三agent仅新增api/multi_draft_confirm_flow_test.go。root负责协议/generated/docs/共同提交和集成。

执行agent在指定独立worktree，不自行commit/merge/push，不改共享协议/迁移/依赖/docs，不跑go test/build或测试审批；编辑/gofmt/diffcheck后交付，root集中验证。测试覆盖不同项键、冻结失败不发Task、不确定后重读/同键重试、成功重放不调Task、结果保存失败恢复、版本/权限/损坏拒绝、其他waiting项仍能编辑。SQL/User/IM/Task替身结果不得写成真实MySQL/云验收。
