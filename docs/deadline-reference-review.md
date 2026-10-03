# 固定参考时刻与有限解析：本批审查

日期：2026-10-03。阶段 6。上一批人工截止时间经用户要求继续，快进合入 main `db951b9`；本批共同协议 `67c6071`，交付在 `codex/deadline-reference-integration`，main 保持 `db951b9` 待用户审查。依据已确认 A51/A52/A53 和[共同契约](deadline-reference-contract.md)。不增加语言、框架、依赖、权限模型、服务边界或迁移。

## 1. 本批实际小步骤

| 步骤 | 改什么、目的与解决的问题 | 文件范围 |
| --- | --- | --- |
| 1 共同契约 | 追加可选参考字段，固定兼容和并行边界，避免三个角色各自定义规则 | proto/pb、共同契约 |
| 2 Agent 请求身份 | 原字段加参考一起摘要，nil 保持旧字节；拒绝非法参考，同键成功重放不调用模型 | prepare RPC/preparer、reference test |
| 3 Go 解析 | 上海日历下只解释完整日期或今天/明天/后天带时分，保留秒毫秒；避免擅补时刻和 24 小时加法 | parser |
| 4 来源及边界 | 指令原文或唯一授权文本消息核对，消息用原发送时间；错误来源拒绝，模糊表达需补充 | parser/test |
| 5 Gateway | 严格区分缺失与正整数，原 Token/键转发，错误在 RPC 前拒绝 | agent_draft、reference HTTP test |
| 6 页面固定展示 | 第一次实际请求前取设备时间，明确上海/UTC参考，提示核对时钟 | chat.html |
| 7 页面失败重试 | 键、身份/范围/指令和参考按会话绑定；已知旧键恢复原值，未知键拒绝猜测；旧结果 epoch 隔离 | chat.html/test |
| 8 主 agent 集成 | 实际 HTTP/TCP gRPC、生产 Agent/存储适配器与 Eino 假模型，验证失败、保存、重放、冲突与资格撤销 | flow test、交付文档 |

### 生成请求调用链

页面首次实际提交固定 `instruction_reference_unix_ms` → Gateway 严格校验并转发 optional 字段 → Agent 经 User 确认当前用户，IM 读取当前授权群上下文 → 以团队/群/指令/参考计算 fingerprint → 查同键记录 → 无记录时走原 Eino 草稿生成及事务保存，有记录返回原运行。参考只改变请求身份输入，不替代权限、服务端创建时间或 Token。模型当前输出格式未变化，也未使用参考来自动填截止时间。

nil 不增加 JSON 字段，原持久摘要逐字节兼容；正数参考纳入摘要。同键变参考或把已含参考的请求改成缺失会冲突，不能悄悄重解释。模型失败尚未保存记录时，服务端没有生成前时间记录；客户端须保留原键/参考。页面按会话记住已知 bundle，可改出再改回同意图的旧键；刷新不恢复 bundle，未知旧键要求新建或凭运行 ID 读取。读取不会伪称返回持久参考。由此承担已确认 A53 的客户端时钟及会话恢复代价。

### 独立解释器调用与结果

`interpretDraftDeadline` 接明确 evidence 与指令参考、授权消息列表 → 校验原文在指定来源中实际存在 → 选原消息时间或指令参考 → 标准库 Asia/Shanghai 规则解释 → 输出原文、来源、参考、时区、状态、UTC 候选及原因。

支持 `YYYY-MM-DD HH:mm`、`YYYY/MM/DD HH:mm`、`YYYY年M月D日 HH:mm`、`今天/明天/后天 HH:mm`，可带秒及 1—3 位毫秒。相对时间按上海日期加天；绝对日期缺指令参考仍能解析，相对日期缺参考为 needs_input。未支持、非法日期、非正 UTC 或夏令时重复/缺失时间为 needs_input；非法结构、未授权/重复/非文本来源或坏参考拒绝，不假装没有时间。时区数据不可加载时返回 Unavailable，不降级固定 +08:00。Date/ZoneBounds 的行为依据[Go 标准库 Date](https://pkg.go.dev/time#Date)和[ZoneBounds](https://pkg.go.dev/time#Time.ZoneBounds)。

这只是内部解释器及测试；尚未从 Eino 输出中调用，也未保存解析元数据/状态。自动预填、歧义处理、持久依据与旧客户端确认保护须下一批一起接线，不能单独把 needs_input 丢成普通 due=0 草稿。

## 2. 全部实际修改文件

相对 main 共 **21 个文件**；没有删除文件。生成输出只在正确 `rpc/agent/pb`，本次 grpc 生成接口字节未变，因此不列为修改。

| 文件 | 作用 |
| --- | --- |
| [api/README.md](../api/README.md) | 生成 HTTP 可选参考与页面恢复边界 |
| [api/agent_draft.go](../api/agent_draft.go) | 严格解析并精确转发可选参考 |
| [api/agent_draft_reference_test.go](../api/agent_draft_reference_test.go) | nil/整数范围/重试冲突测试 |
| [api/deadline_reference_flow_test.go](../api/deadline_reference_flow_test.go) | 实际 HTTP/TCP gRPC、生产 Agent/Eino/SQL适配器组合 |
| [docs/agent-deadline-design.md](agent-deadline-design.md) | 人工批次 main 合入的历史记录 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A51—A53 本批落地、备选/代价与状态 |
| [docs/deadline-reference-contract.md](deadline-reference-contract.md) | 共享字段、兼容和三个执行角色边界 |
| [docs/deadline-reference-review.md](deadline-reference-review.md) | 分步、调用链、完整文件及验证范围 |
| [docs/project-plan.md](project-plan.md) | 实际进度与下一项 |
| [docs/stage6-acceptance.md](stage6-acceptance.md) | 本地验证补充及真实验收条目 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | 三分支、时间窗口与集成记录 |
| [examples/chat.html](../examples/chat.html) | 固定参考、展示、会话 bundle 及旧结果隔离 |
| [examples/chat.test.cjs](../examples/chat.test.cjs) | 原115加11项参考与重试用例 |
| [rpc/agent/README.md](../rpc/agent/README.md) | optional RPC、摘要兼容及解析器边界 |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 生成请求新增 optional 字段4 |
| [rpc/agent/draft_deadline_parser.go](../rpc/agent/draft_deadline_parser.go) | 纯 Go 有限解析及来源验证 |
| [rpc/agent/draft_deadline_parser_test.go](../rpc/agent/draft_deadline_parser_test.go) | 日期、范围、固定参考、DST和证据安全 |
| [rpc/agent/draft_prepare_rpc.go](../rpc/agent/draft_prepare_rpc.go) | 传递字段存在性，不默认0 |
| [rpc/agent/draft_preparer.go](../rpc/agent/draft_preparer.go) | 参考校验及新旧 fingerprint |
| [rpc/agent/draft_reference_test.go](../rpc/agent/draft_reference_test.go) | 旧字节兼容、模型失败/重放/冲突回归 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 正确目录生成 optional Go 结构 |

## 3. 实际验证与未验证

- `go test ./... -count=1` 全量通过。
- `node --test examples/chat.test.cjs` **126/126** 通过，新增 11 项覆盖首次取钟、跨上海午夜、身份/范围/指令变更、新/旧键、刷新未知键、非法时钟、旧成功/失败及跟随读取隔离。
- Linux Agent/Gateway 编译通过，`git diff --check` 通过。
- 组合测试走真实本机 HTTP/TCP gRPC 和生产 Agent prepare/store 与 Eino；SQL、User/IM、模型为替身。首个模型失败后原参考重试保存，数据库期望核对确实含参考的摘要；同键重放不调模型，改参考/省略参考409，资格撤销403。
- 解析器覆盖跨午夜/月/年/闰年、秒毫秒、有效UTC范围、23/25小时日历加天、1986/1991上海历史夏令时、未授权/非文本/重复来源和大整数消息ID。测试显式输入参考，无等待到真实午夜。

Go 默认缓存/标准库访问初次受沙箱拒绝，按工具审批用已有环境重新执行后通过，未安装工具或改变 Go 配置。没有运行真实数据库、生产进程、浏览器、容器或方舟模型；未做迁移、云同步或推送。尚无解析依据持久保存/GET读回、模型原文提取或业务自动填截止时间；阶段6未全部完成。

## 4. 并行记录

| 角色 | UTC开始 → 完成 | 耗时 | 交付提交 |
| --- | --- | --- | --- |
| 解析后端 | 13:23:05 → 13:30:51 | 7分46秒 | `81b71c4` |
| Gateway | 13:23:32 → 13:27:53 | 4分21秒 | `04e2f1f` |
| 页面 | 13:26:28 → 13:35:52 | 9分24秒 | `5afe426` |

从首个角色开始到最后交付的并行窗口 **12分47秒**，不含共同协议、主 agent 后端接线、集成验证及文档耗时；无单 agent 同任务对照，不声称倍数提速。三个 worktree 保留，无冲突合入本批集成分支，main 合入须用户审查。

后续记录（2026-10-03）：用户要求继续后，本批 `89fdceb` 已快进合入 main。上述“尚未接模型/持久依据”是交付时历史；下一批已完成[自动时间闭环](deadline-auto-review.md)，新结果位于独立集成分支待审查，本批文件与验证记录不追改为新批次范围。
