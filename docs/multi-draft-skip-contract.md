# 多项草稿本人显式跳过共同契约

2026-10-04。沿已确认A37/A50/A54/A56，接未提交项显式跳过，保留记录，不创建或取消Task、不删除/重排项。上轮确认91ff7ef已合本地main。本轮只接后台/HTTP及必要混合状态保护；页面按钮、进度展示与A55逐项回帖后续，不新增迁移、依赖、权限角色或后台执行。

## 身份、状态与版本

SkipTaskDraftItem(run_id正,optional item_index必有0..4,expected_revision正)，返回既有GetTaskDraftItemResponse。复用ConfigureDraftAccess，经User本人身份和IM当前群资格核对，完整collection加载、验证实际目标存在。single409，非本人404/群撤权403，缺字段400，过时409。跳过无需负责人资格查询、模型、Task或机器人配置；未处理的重名/needs_input候选可以拒绝，不要求先补成可创建项。

waiting_confirmation且空Task键/ID0才可首次跳过；已skipped且同版本的本人重复操作幂等成功。creating/succeeded始终409，不能以跳过取消不确定操作。不提供撤销跳过或恢复编辑入口。skipped必须空Task键/ID0，其内容/完整元数据仍合法，但可保留ambiguous/not_found/truncated/needs_input；读取/内部锁共用完整校验，不能将未处理依据套入creating/succeeded。

沿A50，跳过是终态操作，内容未改变，revision不递增；仍严格核对expected_revision，响应版本精确相同。备选跳过加版本会把纯生命周期与内容版本混用，增加重试快照变化。本方案保留同内容同版本跳过重放，状态本身阻止编辑/确认。MaxInt64亦可跳过，无版本溢出。原run范围/状态/项数、索引/指纹、草稿所有文字/负责人/时间证据不变。集合头仍waiting，所有项已succeeded/skipped只表示候选处理完，不代表全部创建或回帖完成；本轮返回逐项事实，不新增汇总状态。

## 事务与并发

授权后短事务复用SELECT完整集合FOR UPDATE。比较范围/项数、目标索引、完整目标内容与正revision，不比较其他项变化。版本/内容改变Aborted；目标creating/succeeded FailedPrecondition；waiting→skipped或已skipped同内容均允许，不能回退。首次只执行：

`UPDATE agent_task_drafts SET status = ? WHERE run_id = ? AND item_index = ? AND revision = ? AND status = 'waiting_confirmation' AND task_request_key = '' AND task_id = 0`

参数skipped,run,index,revision；受影响恰好1，否则Aborted/回滚，SQL/提交失败Unavailable。已skipped重放不UPDATE。不得改agent_runs、版本或其他项，不删除记录。事务内无跨服务调用。资格检查与MySQL事务非跨服务原子，沿原授权边界；同轮小集合锁可能短暂串行。

读取合法waiting/skipped均空键/ID0，creating固定键/0、succeeded固定键/正ID；损坏或未知状态Unavailable。共享编辑target包括skipped，409禁止；指定项确认明确拒绝skipped，不调用Task或查询目标成员；其他waiting仍可编辑/确认。确认锁竞争中如果skip赢，不得冻结；skip锁竞争中如果freeze赢，不得跳过。合法其他项推进不构成当前版本冲突。

## HTTP与结果

POST `/api/v1/agent/runs/:run_id/drafts/:item_index/skip`，15秒路由，Agent复用12秒预算。Bearer及规范run/index，正文仅 `{"expected_revision":"3"}`，字段必有正十进制字符串；拒绝null/未知字段/第二JSON。成功检查准确run/正scope/count1..5/显式index且实际存在、完整合法skipped项/TaskID0、reply_msg_id空。skipped回复统一disabled，因为不应回帖；其余项沿现有disabled/not_started规则。版本必须等于提交版本，不接受+1或跳变，不补字段/假成功，异常502；RPC错误沿collection脱敏映射。

GET集合/指定项允许合法skipped，输出原内容与版本；编辑成功仍waiting/task0，确认成功仍succeeded正ID，不能因共享shape扩大而放松操作结果。本轮不改旧single/页面和生成指纹或Task/消息键。

## 七步与工作边界

主agent契约/协议1；后端授权与跳过事务、混合读取/编辑/确认保护2；Gateway跳过入口、结果与读取保护2；实际HTTP/TCP gRPC组合1；root集中审查回归1。三个执行worktree共用同一提交。后端仅Agent非生成collection skip实现/测试与必要store/confirm/rpc响应接线；Gateway仅api collection skip实现/测试、集合校验与main路由；组合仅新api/multi_draft_skip_flow_test.go。

执行agent不改协议/生成/docs/依赖/迁移，不自行commit/merge/push或跑go test/build/测试审批，只编辑/gofmt/diffcheck，由root集中验证。覆盖合法歧义跳过/同版本重放、Max版本、证据保留、状态/版本/当前权限拒绝、SQL0/2行回滚、skip与freeze锁竞争、其他项继续操作、无Task/模型/回帖副作用。替身测试不当真实MySQL/模型/云验收。
