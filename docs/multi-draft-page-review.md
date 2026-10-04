# 原生多项草稿页面审查

2026-10-04。沿已确认原生页面与A41/A46/A50/A52/A53/A54/A55/A56，[共同契约](multi-draft-page-contract.md)。上轮完整e17d97a已快进合本地main；共同e2aa25a统一controller接口、三脚本职责和测试辅助，三个worktree并行，root审查保存core aa07c91、actions 99a8b33、view ebb85c8，按顺序无冲突整合。root追加Gateway嵌入/固定资源路由及三组页面流程为业务814b23f，验证结果如下。当前codex/multi-draft-page-integration供审查，main仍e17d97a；未push、执行迁移或云同步，三个worktree保留。

## 九步实际成果与目的

1. 共同契约：保持原single区，新collection区独立Run/key/输入/状态；统一core/actions/view方法和测试辅助。解决并行文件之间签名、状态或模式混用问题，不加框架、依赖、协议或表。
2. 生成/读取：新集合生成和集合/目标GET，严格1—5连续项、64位字符串ID/版本、全部九时间字段和规范项消息ID。固定首次key/指令/scope/reference及已知生成run，同key不能换run；坏后项不部分接纳。
3. 本地审查状态：逐字段保留各项未保存输入，目标reload与GET审查标记独立，冻结项只读；显式成员分页/精确ID/严格游标，当前scope/epoch和操作ID阻止旧响应覆盖。读取故障保留已知Task及输入。
4. 三类保存：每项文字、负责人、截止时间独立PUT，正版本和完整返回依据检查；正负责人来自显式目录，0明确未指派；时间按AsiaShanghai解释、空明确不设。成功只规范已提交且未再次更改的输入，不清其他字段/项。
5. 独立终态操作：逐项确认发送完整六字段审查快照；未提交项可显式跳过，冻结/成功项禁止；回帖仅成功GET审查后的pending/not_started目标可手动重试空POST。故障只目标必须重读，不自动创建、确认下一项或重发。
6. 卡片展示：新增独立多项面板；安全textContent呈现固定人类序号、草稿/版本、原称呼/解析、九时间依据、精确TaskID与独立回复状态。复用DOM卡片，输入通知不重建控件/丢焦点；无业务内容innerHTML。
7. 按钮与汇总：busy/currentRun/scope/reload/dirty/歧义/GET审查均约束按钮；明确分任务候选处理与回复受理，全部跳过不声称任务创建，accepted非送达。旧clear仅上下文变化时清collection，同群single操作仍独立。
8. 嵌入与组合：Gateway固定三个同源脚本资源，从编译嵌入源码返回，不按请求任意读取文件；新增三组生产原生脚本/原HTML配合DOM与HTTP替身的完整业务流程。
9. 集中审查：root统一提交/整合/检查，无子agent测试审批等待；Node187、全量Go和LinuxGateway通过。更新选型/方案/验收/分工文档及全部实际文件，无推进群内@AI。

`/demo/chat 多项区 → core固定生成key/reference → Gateway集合POST/GET → 独立项卡片 → 本人指定项三PUT/确认/跳过 → Gateway→Agent既有本人/当前群授权/版本事务→Task结果及回帖`。

`目标回帖失败 → 保留Task成功 → 本人GET该项 → 明确POST目标reply/retry（空body）→ 原卡片/固定消息ID`；无Task重建、后台重试或隐式批量创建。回帖流程沿前批[Agent闭环审查](multi-reply-agent-review.md)。

## 实际验证与限制

core先单独 `node --test examples/multi-draft-core.test.cjs` 10/10通过；完整整合运行：

```text
node --test examples/chat.test.cjs examples/multi-draft-core.test.cjs examples/multi-draft-actions.test.cjs examples/multi-draft-view.test.cjs examples/multi-draft-flow.test.cjs
go test ./... -count=1
CGO_ENABLED=0 GOOS=linux go build ./api
```

Node187/187通过（原single/聊天140＋core10＋actions17＋view17＋流程3），全量Go和LinuxGateway通过。未变IM/Agent构建不重复运行，原后端Go链路在全量回归中保持。根Go资源检查验证三个嵌入内容、JavaScript MIME、no-store和HTML同源引用；Go格式/diff检查通过。最终业务814b23f之后仅文档更新，未追加业务代码。

页面测试执行生产三个经典脚本和原HTML内联脚本，DOM/HTTP为替身，无真实浏览器或网络调用：覆盖坏集合/大ID/原时刻重试/knownrun保护、各项dirty保存与版本、模糊人工处理、受理ID/状态、403/409/不确定响应、双击/旧scope/epoch、目录与游标、XSS安全显示、输入DOM复用、全跳过和处理/回帖分离。原140用例仍只加载原内联脚本，guard hook保证旧功能独立兼容。

三组根流程使用所有新脚本和view刷新：三项分别编辑、两创建一跳过，另一项输入及故障reload不互相影响；回帖响应丢失保留TaskID，GET后只重发目标卡片且确认次数保持2；全部跳过保留依据/版本、拒绝创建/回帖；原页面Token hook丢弃lateGET，同scope旧single清理不抹新collection。测试HTTP调用为fetch替身，不冒称页面接真实Gateway/数据库联调。

真实浏览器焦点/布局/控件/datetime-local行为、真实模型生成、MySQL迁移/并发/权限撤销、Kafka/Push/历史去重、证书/容器及最终腾讯云同步仍未验收。既有019/020/021仍须按部署计划核对，未新增或执行迁移；不修改Compose/依赖/服务边界。页面首次生成reference依设备时钟且需本人检查，刷新后未知key不重新发明reference，可按Run ID读取。

本轮交付的是多项页面显式操作，不是群消息自动触发。群内@AI规则/触发归属/去重及运行恢复须先讨论；真实端到端验收仍后续，阶段6不标全部完成。

## 全部实际修改文件

相对main e17d97a共 **20文件**。主要新增三个小原生脚本，HTML仅新增独立面板/脚本引用/上下文hook；其他为必要资源接线、测试和文档，旧single测试及后端业务不改。

| 文件 | 实际作用 |
| --- | --- |
| [examples/chat.html](../examples/chat.html) | 独立多项面板、三个同源script和旧clear轻量hook |
| [examples/multi-draft-core.js](../examples/multi-draft-core.js) | 生成/读取/严格校验、固定意图、逐项输入和成员目录 |
| [examples/multi-draft-core.test.cjs](../examples/multi-draft-core.test.cjs) | 10组完整校验/请求固定/输入/上下文/分页业务测试 |
| [examples/multi-draft-actions.js](../examples/multi-draft-actions.js) | 三类逐项保存、完整快照确认、跳过、GET后原回帖 |
| [examples/multi-draft-actions.test.cjs](../examples/multi-draft-actions.test.cjs) | 17组版本、返回事实、独立失败/恢复及输入保留测试 |
| [examples/multi-draft-view.js](../examples/multi-draft-view.js) | 安全卡片/九时间依据/按钮事件和处理/回帖分离统计 |
| [examples/multi-draft-view.test.cjs](../examples/multi-draft-view.test.cjs) | 17组真实view结合core/actions的DOM/HTTP替身测试 |
| [examples/multi-draft-test-helper.cjs](../examples/multi-draft-test-helper.cjs) | 共用原HTML/script沙箱、DOM及业务fixture/响应辅助 |
| [examples/multi-draft-flow.test.cjs](../examples/multi-draft-flow.test.cjs) | 三组生产脚本完整页面流程/故障恢复/上下文隔离 |
| [examples/chat.go](../examples/chat.go) | 构建时嵌入三份新脚本源码 |
| [api/chat_demo.go](../api/chat_demo.go) | 固定资源处理器，明确MIME/no-store，不读取请求文件 |
| [api/chat_demo_test.go](../api/chat_demo_test.go) | 三脚本嵌入内容/类型/缓存/同源HTML引用检查 |
| [api/main.go](../api/main.go) | 三个精确GET脚本路由，不改业务接口预算 |
| [api/README.md](../api/README.md) | 多项页面使用、明确意图和独立重读/恢复限制 |
| [docs/multi-draft-page-contract.md](../docs/multi-draft-page-contract.md) | 共同controller/HTTP/状态/分工与九步 |
| [docs/multi-draft-page-review.md](../docs/multi-draft-page-review.md) | 本文：全部实际文件、验证和边界 |
| [docs/architecture-decisions.md](../docs/architecture-decisions.md) | 已定原生栈内的方案/备选/理由/代价与验证关联 |
| [docs/project-plan.md](../docs/project-plan.md) | 页面实际成果、阶段状态和下一步先讨论@AI |
| [docs/worktree-collaboration-plan.md](../docs/worktree-collaboration-plan.md) | 三绝对目录/允许文件/提交/整合和验证记录 |
| [docs/stage6-acceptance.md](../docs/stage6-acceptance.md) | 页面本地替身验证及真实浏览器/链路验收项目 |
