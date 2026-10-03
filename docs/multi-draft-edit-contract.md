# 多项草稿逐项编辑共同契约

2026-10-04。上轮保存/读取结果c24dc97已合main，本批沿A37/A50/A51—A54，只接本人逐项文字/负责人/截止时间编辑，不接确认、跳过、回帖或页面。复用019列和原生产ConfigureDraftAccess；没有新迁移、模型或依赖。三个执行worktree共同起点必须含本契约及协议。

## RPC与身份

新增 EditTaskDraftItemText(run_id,optional item_index,title,description,expected_revision)、SelectTaskDraftItemAssignee(run_id,optional item_index,optional assignee_id,expected_revision)、EditTaskDraftItemDeadline(run_id,optional item_index,optional due_at_unix_ms,expected_revision)，均返回既有GetTaskDraftItemResponse。index必有且0..4，revision正，ID与due必有且合法，0分别明确未指派和不设时间；run正。本批沿原draftReadTimeout12秒和Gateway15秒预算，不增加超时或后台执行。原单项方法保持，collection入口拒绝single。

每次经User确定Token本人，完整读取并验证collection、确认目标存在、IM核对当前团队群资格；版本与项waiting/空Task键/ID0均核对。正负责人以原Token复用User CheckTeamMemberByID，沿该专用检查RPC的成功语义，要求响应非nil（既有响应无ID字段）；0不查目标成员。外部RPC不在SQL事务内，模型不参与。缺本人运行404、越群资格403、single/冻结/版本耗尽409、过时版本Aborted→409。不得仅靠HTTP端挡住非法RPC。

## 事务和独立版本

读授权后短事务重新读取完整collection FOR UPDATE，复用现有完整性校验（可抽内部load helper），再次比较mode/count/scope、目标稳定索引、目标revision、完整目标草稿、waiting状态/空Task键/ID0。MAX5项，事务锁可能串行同轮不同项的写入，但不得因其他项版本变化拒绝当前目标；不比较整个集合各项内容。备选只锁目标可减少竞争但需另写完整性校验，本批先共用小集合校验，后续真实性能数据再优化。权限检查与DB事务非跨服务原子，沿现有规则每次操作前查。

标题/说明TrimSpace后沿200/2000 Unicode限制，其他字段不变。负责人正ID保存selected、0保存unassigned，原称呼与其他字段不变；相同已selected/unassigned ID no-op，但matched→selected或none/ambiguous→unassigned即使数值相同也属本人选择，应加版本。截止正值保存selected、0保存unset，完整原始九字段中只resolution变，其余text/source/sourceID/reference/timezone/reason/parsed/首次reference不改；needs_input0→unset也加版本。当前目标实际变化只将该项revision+1，其他项不增；相同值和相同处理状态仍锁定核对但不UPDATE。MaxInt64仅拒绝实际变化，no-op可成功。原来源ID、run状态/项数/指纹、索引、原证据、Task键/结果不可改。

固定UPDATE参数供组合测试核对，WHERE均为run_id=? AND item_index=? AND revision=? AND status='waiting_confirmation'，受影响恰好1，否则Aborted并回滚：

- 文字 SET title=?, description=?, revision=revision+1（参数title,description,run,index,旧revision）。
- 负责人 SET assignee_id=?, assignee_resolution=?, revision=revision+1（ID,resolution,run,index,旧revision）。
- 时间 SET due_at_unix_ms=?, deadline_resolution=?, revision=revision+1（due,resolution,run,index,旧revision）。

事务内返回准确保存的目标，不重读拼凑不同版本，响应携带run/team/group/item_count及显式item_index，完整newmetadata和正revision，waiting/task0、disabled或not_started/空msg。更新报错回滚，不向调用者报告保存成功。

## HTTP与验证

三个PUT：`/api/v1/agent/runs/:run_id/drafts/:item_index`（文字）、其`/assignee`与`/deadline`。Bearer唯一原Token，index规范0..4、run规范正字符串。正文分别{title:string,description:string,expected_revision:string}、{assignee_id:十进制非负字符串,expected_revision:string}、{due_at_unix_ms:JSON整数0..253402300799999,expected_revision:string}。所有字段必填，拒绝null/额外字段/第二个JSON；说明允许空但不可缺，title/description规范trim后转发；版本规范正十进制。沿原错误脱敏映射，Aborted/FailedPrecondition409。

成功复用集合项严格shape验证：run=请求、scope正、count1..5、index=请求且<count、完整waiting项、ID/revision字符串、负责人和九字段时间合法。revision为输入revision或安全+1，不能跳变/回退。文字结果须等于提交规范文字；负责人结果等于提交ID且selected/unassigned相符；时间等于提交due且selected/unset相符。错误/缺身份/部分依据或服务假成功502，不自己补字段。没有expected旧文字字段：A50整项版本和事务完整比较已防覆盖，旧接口body不改。

## 工作分工与八步

主agent共同协议/生成代码/文档1步；后端文字/负责人/时间3步；Gateway解析转发与路由/结果校验2步；组合1步；根审查回归1步。后端仅rpc/agent非生成collection编辑及必要store/access接线/测试；Gateway仅api新collection编辑handler/test、main路由及必要小helper，不改组合文件；第三agent仅新增api/multi_draft_edit_flow_test.go，实际HTTP/TCP gRPC生产Agent+SQL/User/IM替身，复用上一轮flow，测试两项独立编辑/版本、歧义明确处理、no-op、过时/并发变化拒绝、成员撤权/群撤权、原证据及其他项不变、single旧保护不退化。

执行agent不自行commit/merge/push、不改协议/生成/迁移/依赖/docs，不跑go test/build或测试审批；只编辑/gofmt/diffcheck交付，主agent集中定向/全量Go、Node140及LinuxAgent/Gateway编译。遇新架构/权限问题暂停报告。真实数据库/模型/浏览器/容器、迁移及云同步仍留最终验收。
