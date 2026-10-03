# 固定指令参考时刻与有限解析：共同契约

日期：2026-10-03。依据用户已明确选择的 A51/A52/A53。本批推进生成参考请求链和独立 Go 时间解释器；不切换 Eino 输出格式、不新增解析元数据列/迁移、不把解释器已测试标为业务自动填时间可用。

## 1. 固定参考请求

`PrepareTaskDraftRequest` 追加 optional int64 `instruction_reference_unix_ms = 4`。HTTP 生成正文追加同名可选数字。缺失保持旧请求 fingerprint 的 JSON 字节/摘要完全一致；显式 null、0、负值、小数、字符串及超过 253402300799999 的值拒绝。合法正 UTC 毫秒与团队/群/指令一起加入新请求摘要。同键改变参考返回原有冲突，已保存同键仍先核对当前权限后返回原运行，不重新调用模型。客户端时间只作解释输入，不作为登录、资格、服务端审计或持久创建时间。

页面第一次实际生成请求前固定 Date.now()；同身份、团队群、指令及键的失败重试保留原值，不每次刷新时钟。展示 Asia/Shanghai 的参考时刻及 UTC，并注明请核对设备时钟。修改指令/群/身份属于不同生成意图，生成新键和新参考；显式新键同样换参考。键、参考、意图一起保存，避免改键后再改回旧键但用新时间；不做跨刷新恢复，不声称刷新后仍可安全恢复未知请求。旧键手动输入的恢复不猜原时间，要求新请求或凭 run ID 读取。现有 HTTP 响应仍只返回 run_id；持久完整解析依据/读回展示下一批接线。

Gateway 只严格解析和转发可选字段，保留缺失与存在；不补服务当前时间，不计算自然语言，不改变原 Token/请求键。已有字段大小、路由预算、run_id 字符串规则不变。

主 agent 负责 Agent prepare 接线、摘要兼容测试、跨层测试和共享文档；后端执行 agent 只负责下一节纯解析/来源验证，避免共同文件冲突。

## 2. Agent 有限解释器

只新增 `rpc/agent/draft_deadline_parser.go` 和 `draft_deadline_parser_test.go`，同包内部使用，不注册新 RPC。提供 `interpretDraftDeadline(instruction string, messages []*impb.TeamGroupMessage, evidence draftDeadlineEvidence, instructionReference *int64) (draftDeadlineInterpretation, error)`。

- evidence 字段：Text string、Source string（none/instruction/message）、SourceMessageID int64。模型原文必须完整在指定来源文本中实际出现；来源 ID 精确属于授权文本消息，不能直接用任务 source_message_id，也不能自行读取其他消息。空表达只能 none/0；部分元数据、非法编码/超长/来源或 ID 拒绝。原文不随意 trim 后伪装证据，首尾空白拒绝，最多 200 Unicode 字符。
- result 字段：Text、Source、SourceMessageID、ReferenceUnixMs、Timezone、Resolution、DueAtUnixMs、Reason。固定 Timezone=Asia/Shanghai；Resolution 为 none/parsed/needs_input；没有时间 none，未支持/非法日期/缺参考/夏令时歧义 needs_input 且 DueAtUnixMs=0，具体原因可区分。结构或证据非法返回 FailedPrecondition，不伪装为 none。
- instruction 引用可缺失；存在须 1..253402300799999。message 只用原授权消息 CreatedAtUnixMs，要求合法正值且唯一匹配 ID；不能用 instruction reference 或 time.Now() 替代。即使绝对日期也保留已验证的来源/参考。
- 首版支持完整日期 `YYYY-MM-DD HH:mm`、`YYYY/MM/DD HH:mm`、`YYYY年M月D日 HH:mm`，以及 `今天/明天/后天 HH:mm`；可带秒及最多三位毫秒。24 小时制，精确验证真实日期和范围；UTC 候选必须为正值。相对表达按固定参考的上海日历日期加天，不按 24 小时秒数计算。
- 不支持“明天下午”、只有日期/时间、下周/月底、未列出的表达，不编造时分。Asia/Shanghai 使用标准时区规则，不能假定永久 +08；不存在/重复墙上时间要求补充。标准库实现，不新增依赖，不调用模型/数据库/网络或 time.Now()。

测试必须覆盖绝对/相对、跨午夜重试/跨月年/闰年、消息与指令参考差别、无参考、模糊/错误日期、范围、秒毫秒、上海历史夏令时、未经授权/非文本/重复消息 ID、非法结构及安全精确大整数来源。

## 3. 分派与交付

共同协议/pb/文档由主 agent；后端仅两新 parser 文件；Gateway 仅 api/agent_draft.go 及新增 api/agent_draft_reference_test.go；页面仅 examples/chat.html 和 examples/chat.test.cjs。三个工作区复用旧目录但用新具名分支。各自先说明步骤，不自行提交或合并。全批目标最多九步：共同契约、Agent 请求摘要、解析、证据回归、Gateway、页面固定/展示、页面重试保护、跨层验证与交付。

全量 Go、页面和 Linux 构建由主 agent 集成后执行。真实模型/数据库/浏览器/容器及迁移仍未验收。下一批在此基础上接 Eino 原文提取、解析依据/状态持久化、本人处理歧义与确认保护；本批没有新增架构选型，不把 A52/A53 的完整落地提前标完成。
