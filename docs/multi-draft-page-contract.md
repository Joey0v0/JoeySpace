# 多项草稿原生页面共同契约

2026-10-04。上轮e17d97a已合本地main；沿已确定原生页面、A37/A41/A46/A50/A52/A53/A54/A55/A56，不加框架、依赖、协议或迁移。整个批次最多九步，只接页面显式生成/逐项审查和独立回帖；群内@AI与真实环境后续。

## 1. 文件和运行方式

聊天原single区保留，新collection区独立Run/key/输入和状态，不把集合送旧single接口。原内联脚本之后依次加载 `/demo/multi-draft-core.js`、`/demo/multi-draft-actions.js`、`/demo/multi-draft-view.js`；root将三份源码通过examples.Chat…嵌入Gateway并提供精确静态路由，不开放目录或任意路径。经典原生脚本可复用现有validID、validDraftDue、validDraftAssignee、validDeadlineMetadata、shanghaiDeadlineInput/parseShanghaiDeadline、taskDraftScope/sameTaskDraftScope，不迁移旧single实现。三份独立小脚本是普通文件分工，备选继续追加大段内联更容易冲突和混淆single状态，代价为三个明确资源路由；仍原生JS和原Gateway部署。

## 2. 共同控制器接口（须严格对齐）

core定义 `globalThis.MultiDraftController`。构造参数 `{fetch, getScope, isScopeCurrent, onChange, now, randomKey}`；now默认Date.now，randomKey默认浏览器crypto随机规范键。scope沿 `{token,teamID,groupID}`。公开字段：epoch、busy、runID、scope、items（原HTTP item数组，连续0..N-1）、edits（Map index→`{title,description,assigneeID,dueInput}`）、reload（Set必须GET的index）、reviewedReplies（Set最后成功GET审查的index）、members（Map userID→member）、memberCursor（字符串）、membersLoaded、requestKey、reference（整数或0）、message（字符串）。无后台/定时器/自动确认或发送。

core公开方法：`invalidate()`清空集合/编辑/成员及消息并增epoch（旧网络结果不可覆盖）；`newRequestKey()`显式开始新生成键；`prepare(instruction)`以当前requestKey首次提交固定reference、原Token与scope，POST集合入口后自动GET集合；同key同instruction/reference重试，不明旧key/更改指令或scope禁止复用并提示新key，不能偷偷新key；`load(runID)` GET完整集合；`loadItem(index)` GET目标，成功后清该项reload并标reviewed；`loadMembers(more=false)`显式团队成员分页；`setEdit(index,patch)`保存本地输入；`dirty(index)`比较原快照；`snapshot(index)`只返回当前授权上下文的原item，不网络。GET保留各项未保存输入，只对实际dirty字段保留；冻结/成功/跳过项不保留可编辑输入。读取失败保留已知Task结果及输入并要求重读，无假成功。

供actions复用：`_notify()`；`_begin(runID=this.runID)`验证scope/上下文并全局busy防重入，返回`{epoch,scope,runID,id}`快照；`_current(op)`须epoch、原scope及run相同；`_end(op)`仅结束自己operation并通知；`_request(op,path,init={})`原Bearer、JSON头（有body才设），检查HTTP/code，返回data；`_replaceItem(item,{preserve=true,reviewed=false}={})`只更新目标、留新dirty输入、清该项reload，reviewed只在GET；`_markReload(index,message)`标该项并保留Task/input。actions不得越界修改core。

`MultiDraftController.validItem(item,runID,index)`和`validCollection(data,runID,scope)`为static。严格ID十进制字符串/64位、显式numeric index、count1..5、连续顺序、正revision、source_id非负规范字符串、完整九时间字段、文字trim与1..200/0..2000 unicode有效、负责人/时间合法。waiting/creating TaskID0，succeeded正TaskID，creating/succeeded负责人/时间须已处理；skipped TaskID0、disabled/空msg，可保留歧义。disabled/not_started/unknown消息空；pending/accepted只成功且规范 `bot-task:<run>`或非0`:index`，unknown只成功。完整响应必须匹配当前run/scope/count和目标身份，损坏不部分接纳。

## 3. actions职责

actions在原类prototype添加 `saveText(index)`、`saveAssignee(index)`、`saveDeadline(index)`、`confirm(index)`、`skip(index)`、`retryReply(index)`，操作全部返回Promise，不自动串联确认全部。所有写入先当前snapshot、无busy、目标无reload；必须目标expected_revision。编辑/skip只waiting，文字/时间/负责人独立保存；选择正负责人必须已显式加载当前目录，0明确未指派（可处理歧义）；日期使用Asia/Shanghai有限解析，空表示本人明确不设，保留原九时间依据。

confirm只waiting/creating（创建不明需本人GET后显式同项重试），dirty/负责人或时间needs_input拒绝；发送完整六审查字段。skip只waiting且无任何未保存输入，版本匹配，原内容保留；无需歧义处理。retryReply只succeeded、pending/not_started且已GET审查、目标无reload，POST空body，绝不confirm/Task。任务成功和回帖未知分别显示，unknown先GET。所有操作只接受目标准确结果/状态/内容/版本/TaskID/原依据，编辑版本同值或+1并须符合实际改动，确认/skip/reply原版本不变；reply受理必精确MsgID。失败（含409、HTTP响应不明、坏成功响应）只标目标reload，保留已知Task和本地输入，不调用其他项。完成或异常时旧context不能写新context。

## 4. view职责

新增HTML面板固定ID `multiDraftInstruction`、`multiDraftRequestKey`、`multiDraftRunID`、`multiDraftReference`、`multiDraftMessage`、`multiDraftSummary`、`multiDraftItems`、`btnMultiPrepare`、`btnMultiLoad`、`btnMultiNewKey`、`btnMultiMembers`、`btnMultiMoreMembers`。view创建`globalThis.multiDraftPage`控制器；`globalThis.invalidateMultiDraftPage()`给旧clearTaskDraftResult轻量hook，保证Token/团队/群变化清除新状态。按钮调用`multiDraftPage.prepare/load/...`并统一显示错误，事件只手动发起，不后台加载成员或确认。

每项用textContent/createElement呈现安全卡片，固定序号不重编号（人类显示第1项，接口0），显示文字/版本、负责人原称呼与解析状态、截止时间及九字段证据、TaskID和独立回帖状态。输入事件写edits，busy时统一禁用编辑/写按钮；每项独立保存文字、负责人、时间；loadItem重读按钮；确认、显式跳过、原回帖重试。创建/跳过按钮不得批量执行。负责人下拉由当前目录生成，原未知ID可显示但不可当新已确认成员保存。处理进度分别计waiting/creating/succeeded/skipped及accepted/pending/unknown等；全部created或skipped才“候选已处理”，全部skipped不声称任务创建，accepted不声称成员送达，creating不声称后台运行。纯显示不得包含Token/内部实现堆叠，不innerHTML插入业务内容。

## 5. 分工和验证

共同1、core2、actions2、view2、root组合1、集中审查1=九步。三个原worktree同共同提交起步，执行者只在绝对目录编辑允许文件，不运行测试/build、不commit/merge/push；root集中测试和整合。

- A `D:/zy/GoLang/go-im/.worktrees/assignee-backend`：只新examples/multi-draft-core.js、multi-draft-core.test.cjs；步骤2生成/读/严格数据/上下文，步骤3独立输入/成员/状态。
- B `D:/zy/GoLang/go-im/.worktrees/assignee-gateway`：只新examples/multi-draft-actions.js、multi-draft-actions.test.cjs；步骤4逐项三编辑，步骤5确认/跳过/回帖。
- C `D:/zy/GoLang/go-im/.worktrees/assignee-ui`：只examples/chat.html、新multi-draft-view.js、multi-draft-view.test.cjs；步骤6面板安全渲染，步骤7事件/按钮/摘要和旧context hook。旧脚本除hook不得修改。
- root：此契约、共享test-helper、examples/chat.go、api/chat_demo.go/test/main.go、组合test、共同文档，统一审查提交。测试辅助可读原内联脚本和指定外部脚本，运行真实JS配合DOM/HTTP替身，不称真实浏览器。旧140回归须保持。
