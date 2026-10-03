# 自动截止时间闭环：共同契约

2026-10-03。按用户已定A51/A52/A53实施。复用解释器、原UTC列、A50版本、A41冻结和Task键。Agent拥有解析依据，不改Task/User数据归属。主agent独占协议/生成代码、018迁移/初始化、文档和api/deadline_auto_flow_test.go；其余角色独立worktree。共同基线准备时协议已生成、SQL已备，业务随后实现；不把协议准备标为功能完成。

## 1. 原文与生成

Eino原四字段之外，新增必填string deadline_text、deadline_source（none/instruction/message）、deadline_source_message_id（规范非负十进制字符串）；只提取完整原文和指定授权来源，不输出UTC、reference、resolution或ID推断。没有时间仅text=""/source="none"/source_message_id="0"；结构缺字段、部分证据、非法来源/未授权文本、非法时间参考拒绝，不当无截止时间保存。

taskDraft追加可比较值字段Deadline，类型draftDeadlineMetadata：Text/Source/SourceMessageID/ReferenceUnixMs/Timezone/Resolution/Reason/ParsedUnixMs/InstructionReferenceUnixMs。模型候选暂只含前三字段；prepare拒绝其他模型提供的可信元数据。调用interpretDraftDeadline核对指定原文和消息来源，以req instruction_reference或原消息时间解释。把解释结果和独立的instruction_reference复制到Deadline，DueAtUnixMs=解释器候选（none/needs_input为0），再校验完整草稿、按原事务保存。无时间也保存Timezone和Resolution=none、InstructionReference，使首次指令参考可读回。指纹、旧nil请求摘要、已保存同键先重放不重新模型/解析保持上一批规则。

## 2. 持久/输出形状

018给agent_task_drafts加九列：deadline_text/source/source_message_id/reference_unix_ms/timezone/resolution/reason/parsed_unix_ms及instruction_reference_unix_ms。默认空/0代表旧草稿，不能默认为新none，也不重算旧任务。初始化同列；读取/锁定都依赖018，升级前执行，但本批不执行真实迁移。写草稿与运行仍同一事务；Agent读取和lockedDraft统一含九列，其他选择/文字编辑/冻结/结果/回帖保留完整Deadline。

TaskDraftItem新增deadline消息（字段9），TaskDraftDeadline九字段见proto。内部旧全空struct返回nil；HTTP旧deadline省略，新返回deadline对象，source_message_id十进制字符串，其他时间JSON整数。页面同形状，source ID不可转Number。完整新对象的source/timezone/resolution/原文/参考/候选/原因须校验，部分或异常对象拒绝。已有due与deadline状态一致，范围上限253402300799999；解析candidate为正数或0。

形状规则（Agent/Gateway/页面一致）：

- 旧Deadline九字段全空/0合法，按旧due/review规则保留；HTTP缺deadline即旧。空对象不能视作旧。
- 新Timezone严格Asia/Shanghai；所有时间0..max，source_message_id>=0；Text合法UTF8/最多200Unicode/不含首尾空白，Reason仅空或解释器的unsupported_expression/invalid_time/invalid_date/nonexistent_local_time/ambiguous_local_time/out_of_range/missing_reference。
- source=none：Text空/ID0/Reference0/Parsed0/Reason空；resolution可none/selected/unset。instruction_reference可0或正（0是旧请求缺值）。
- source=instruction：Text非空/ID0/Reference等于InstructionReference（绝对日期可0）；source=message：Text非空/ID正/Reference正且独立于InstructionReference。
- expression的原结果：Parsed正+Reason空，或Parsed0+非空合法Reason；本人处理保留这组原依据。
- resolution=none要求source=none且due0；parsed要求expression且due=Parsed正；needs_input要求expression、Parsed0+Reason非空且due0；selected要求due正且原依据合法；unset要求due0且原依据合法。selected/unset保留原文、来源、原参考、原候选和原因供审查。

## 3. 本人处理和确认

复用专用EditTaskDraftDeadline，不加新RPC：新依据草稿保存正时间设resolution=selected，明确0设unset；其余八项原依据不变。即使needs_input due0→明确unset due0，状态变仍版本加一；重复同状态同due为no-op。旧Deadline全空按旧保存/no-op行为保留，不为旧数据编造证据。只允许本人当前群权限和waiting_confirmation；完整锁/版本比较，文字/负责人保存不能绕过或改时间依据。

Confirm新增string expected_deadline_resolution字段7（HTTP同名可选字符串）。旧草稿缺/空保持旧兼容，新依据必须为当前状态且显式expected_due（含0）；缺状态FailedPrecondition，错状态Aborted，needs_input无论请求怎样均拒绝调用Task。非法状态InvalidArgument。在编排及锁定freeze都做状态/时间/版本核对；creating/succeeded重试不重算或修改依据。冻结Task仍只交当前due，Parsed原候选不代替本人修改结果。完整版本保护原依据，不增加跨服务事务或后台重试。

Gateway共享草稿GET/编辑/确认/回帖输出新增完整deadline，并验证形状；编辑成功的有新元数据结果须selected/unset且时间正确。确认成功须审查状态一致，旧空兼容；不处理自然语言或授权。deadline meta异常502；正常冲突仍409。

## 4. 页面审查

旧缺deadline可沿既有due/revision读写；新对象完整校验，安全以textContent显示时间原文、来源（instruction或精确消息ID）、解释参考/时区、原候选/原因、当前处理状态以及独立的首次指令参考。持久依据与当前生成请求参考两者区分；不把旧对象缺字段补none。

needs_input禁止确认，提示补完整时间后保存或明确清空保存。输入空不等于已处理；点击Save deadline(0)取得unset和新版本后才可确认。parsed候选仍本人看后确认，selected/unset显示本人覆盖且保留原证据。确认携带快照expected_deadline_resolution（legacy可省略）和原due/版本，未保存文字/负责人/时间继续阻止确认。所有现有epoch/互斥/失败重读/旧结果保护保留。其他保存/确认/回帖响应不得悄悄更改原依据；deadline保存只允许改状态和due。日期输入用上一批上海转换，不再扩展语言/规则。

## 5. 角色/小步/验证

后端最多三步：模型及prepare接解析；持久/输出及形状；本人状态处理及确认保护/业务回归。允许rpc/agent非生成实现和必要测试、cmd/agent测试，禁止协议/生成/迁移/docs/依赖；不要改api测试。

Gateway最多两步：共享meta输出验证；确认审查/编辑返回及定向测试。允许api Go实现及Gateway测试；禁止主agent预留api/deadline_auto_flow_test.go与api/deadline_reference_flow_test.go。

页面最多两步：依据展示及校验；本人处理/确认保护和回归，仅examples/chat.html/chat.test.cjs。主agent契约1/集成1，全批最多九步；不得自行commit/merge/push或执行生产数据库/模型，各步骤先解释，遇到关键新选择/越界停止受影响部分报告。

Agent模型变必填七字段，必要已有假模型测试追加合法none证据，保留原业务断言（不得删旧测试/减弱断言）。SQL替身涉及生成INSERT追加九列参数，现有读取row可全空代表legacy，新测试需验证实存/实读/lock状态。主agent补实际HTTP/TCPgRPC/Eino假模型+SQL/业务替身，覆盖parsed及needs_input、持久参考、选择/清空版本、旧确认拦截、响应丢失冻结重试。全量Go、Node和Linux构建；真实MySQL/方舟/浏览器/容器仍未验收。
