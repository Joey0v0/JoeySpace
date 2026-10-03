# Agent 草稿截止时间：用户已定方案

日期：2026-10-03。状态：用户已明确选择 A51/A52/A53 三项 A；本批先实施人工截止时间编辑与审查，提取/有限解析及指令参考接线后续小步实施。未执行真实迁移或模型请求。依据 [项目计划](project-plan.md) 6.2、已确定 A28 的 UTC 时间点、A50 的整份草稿版本和 [架构记录](architecture-decisions.md)。

## 1. 选型讨论时的已有能力与缺口

Agent 草稿及 Task 都有 `due_at_unix_ms`，0 为未设置，合法上限为 253402300799999；任务表保存 UTC 毫秒。Task 已校验范围、将非零时间纳入幂等摘要；Agent 冻结及重试沿用持久字段，无须更改任务的数据归属。人工任务页面按浏览器本地时区输入和显示。

Agent 模型当前禁止输出截止时间，草稿页面没有时间编辑/审查快照。授权群消息包含发送时间，但生成请求没有指令的固定参考时刻；运行表 `created_at` 在生成完成后写入，不能代替指令时间。当前去重在成功保存草稿后稳定，不能假设首次模型失败后已经保存参考时刻。时间证据可能来自另一条消息，不应直接借用任务的 `source_message_id`。

共享 Gateway 草稿输出及集成页 `validTaskDraftData` 目前没有截止时间范围校验，后续在既定 UTC 契约内补齐。`due_at=0` 无法区分“没有提时间”和“提了时间但不明确”，自动识别需保存原文、来源/参考、时区及处理状态供本人审查；仅人工时间编辑可复用现有列。

## 2. A51：解释时区

| 方案 | 收益 | 代价与影响 |
| --- | --- | --- |
| A：首版应用统一 Asia/Shanghai（推荐过渡） | 国内团队解释一致；不新增团队配置功能 | 页面明确标识，服务器/浏览器时区不隐式参与解释；未来跨地区团队再讨论团队时区。此选择不是现有聊天时区自动成为团队配置 |
| B：现在增加团队时区配置 | 更符合跨地区协作和最终团队时区方向 | User 拥有配置，需要数据、权限、接口和迁移；旧草稿保存当时解释依据，配置变更不能重算 |
| C：请求携带浏览器时区 | 贴近本人设备输入 | 同群语句可能因设备产生不同解释，不推荐用它解释别人发的消息 |

用户已选择 A，仅用于本阶段 Agent 解释和编辑，并明确显示时区；人工 Task 已有的浏览器本地时区输入不自动改变。团队时区目标仍保留，A 属首版显式过渡。

## 3. A52：模型与时间解释的边界

| 方案 | 收益 | 代价与影响 |
| --- | --- | --- |
| A：模型提取原文/来源，Go 有限规则解释（推荐） | 有依据、可重复测试；模型不生成时间戳 | 首版支持范围有限；保存原文、参考依据及状态需要 Agent 字段/迁移和各层展示 |
| B：模型直接给 UTC 候选 | 自然语言覆盖更广，初期解析代码少 | 真实模型可能算错日期/时区或补出时刻，仍须原文核对和人工审查，不能把提示词当保证 |
| C：本批先只做人工截止时间编辑 | 少改动，复用现有列，不增加自然语言元数据 | 暂不实现 AI 从讨论中识别时间，后续另做解析链 |

推荐 A 的首版范围：带完整年/月/日和时分的明确日期时间，以及有固定参考依据的“今天/明天/后天 HH:mm”等有限表达。不支持的表述展示为需要补充，不能声称全面理解自然语言。没有提时间可以保留未设置；“明天下午”、只有日期、只有时刻且缺日期、下周/月底等不自动补 15:00 或 23:59。本人可以填完整日期时间，或明确选择不设截止时间后确认；缺证据或非法模型结构须拒绝，不伪装为没有时间。

## 4. A53：指令里的相对时间怎样固定

群消息里的相对时间以那条授权消息的发送时间为参考，沿用项目计划。以下只讨论本人新生成指令里的“明天”：

| 方案 | 收益 | 代价与影响 |
| --- | --- | --- |
| A：页面首次提交固定参考时刻，与生成键一起重试（推荐较小方案） | 首次失败后跨午夜重试也保留同一参考；不增加生成前运行状态 | 新请求字段纳入指纹，页面保留键和参考值；客户端时钟可能有误，需展示本人审查。该值仅是时间解释输入，不是授权或服务端审计时间 |
| B：Agent 在模型前持久保存首次服务端时刻 | 不依赖客户端时钟，同键读取原参考 | 增加生成前持久记录/状态及失败恢复规则，改变现有仅成功保存草稿的生成流程，范围较大 |
| C：首版指令相对时间由本人补填 | 不引入固定参考请求协议 | 消息相对时间仍可解释，指令相对时间无法自动填日期 |

不使用每次 `time.Now()` 重新解释同键请求。A 若被选择，新字段缺失的旧请求仍保留原摘要规则，不能给旧运行换键；无固定指令参考的相对表达只能要求补充，不能偷偷取服务当前时刻。同键重放已持久草稿不重新请求模型或解析。确认后始终使用冻结 UTC 值及原 Task 键，不因跨午夜、服务重启或后续配置变化重算。

## 5. 已选方案的完整路线与并行边界

1. 主 agent 固定选项、状态、来源证据、旧客户端行为、RPC/HTTP、必要 Agent 迁移及共同提交；先做人工编辑，再接有限提取/解释，不在一次批次实现所有时间表达。
2. 后端 agent 负责 Agent 时间字段/存储、有限解析、版本保护的本人时间编辑和确认；不能直接读 User/Task 表。生产和测试注入时钟的接口细节在既定方案内统一。
3. Gateway agent 负责时间元数据、专用保存入口、确认审查值及共享响应范围检查；RPC 替身不替代授权验证。
4. 页面 agent 负责原文/参考/时区/结果展示、显式保存或不设时间、确认快照和上下文保护；输入按选定解释时区转换，不能直接复用浏览器 `new Date(datetime-local)` 当团队时间。
5. 主 agent 集成跨午夜、明确/模糊时间、错误日期、同键重试、版本冲突和冻结结果不变等验证，再交付用户审查。批次上限仍最多九步，不要求凑满。

预计路径：Agent `eino_task_draft.go`、`draft_preparer.go`、草稿形状/存储/确认与专用编辑；Gateway `agent_draft.go`、专用处理器、确认与路由；页面仅 `examples/chat.html` 和 `chat.test.cjs`。具体允许文件、迁移和生成代码由选定后的共同契约逐个列出，不在待决定阶段生成或执行。

## 6. 选型准备阶段的只读审查记录

用户在已交付负责人结果后要求继续，主 agent 将 `main` 从 `7a717cc` 快进到已验证 `d43d644`，工作树与集成提交逐文件一致，无冲突、无业务代码新改动；没有重复运行同一套测试或推送远程。

三个执行 agent 仅做各自目录的只读检查，共同开始记录为 12:21:18 UTC：Gateway 完成 12:22:51（约 1 分 33 秒），页面完成 12:23:02（约 1 分 44 秒），后端完成 12:23:19（约 2 分 1 秒）。这表示选型准备阶段三项并行审查的时间窗口，不是整轮耗时，也不是与单 agent 对照得到的提速比例。分析依据为现有源码；该只读阶段没有新运行数据库、模型或页面测试。用户随后明确选择三个 A，普通实施契约见 [人工编辑共同契约](deadline-collaboration-contract.md)；选项表保留讨论依据，人工编辑的实际交付见第 7 节。

## 7. 人工截止时间闭环审查

日期：2026-10-03。共同基线 `1cdfaa8`；本批在 `codex/deadline-integration` 完成，main 仍为上一批已审查结果 `d43d644`。以下共 34 个实际修改文件，相对 main 统计，含共同协议、实现、测试及交付文档；没有删除文件、新依赖或新迁移。A51 的人工显示/输入已接线，A52 的自动提取/解析与 A53 的生成参考时刻字段尚未实现。

| 小步骤 | 做什么、目的与解决的问题 | 实际文件范围 |
| --- | --- | --- |
| 1 契约 | 固定 UTC 毫秒、明确零、版本及审查规则，使三个执行分支遵守同一接口 | 协议、pb、共同契约及决策/计划 |
| 2 后端写入 | 本人获当前群权限后，短事务锁定完整草稿，只改截止时间；避免越权、并发覆盖 | deadline RPC/store、draft 接线 |
| 3 后端确认 | 同时核对版本与审查时间，再用冻结值调用 Task；避免旧页面盲确认 | confirm RPC/store、task_draft |
| 4 后端回归 | 覆盖清空/no-op、错误时间、版本耗尽、事务内冲突与冻结重试 | Agent 测试及启动替身适配 |
| 5 Gateway 保存 | 增加专用 PUT、严格整数与共享响应范围校验；避免 null 被当作清空 | deadline handler、共享草稿及路由 |
| 6 Gateway 确认 | 区分审查字段缺失和明确 0，校验成功结果，保留错误映射 | confirm handler 和定向测试 |
| 7 页面编辑 | 显示上海时间，提供毫秒精度输入、独立保存/清空；避免设备时区与精度影响 | chat.html |
| 8 页面审查 | 保存快照、待保存阻止确认、上下文/互斥保护，确认携带已审查值 | chat.html 与 chat.test.cjs |
| 9 主 agent 集成 | 合并三分支，实际 HTTP/TCP gRPC 加业务/SQL 替身验证保存到确认重试，整理交付 | 组合测试和各说明文档 |

调用链：页面按 Asia/Shanghai 将明确日期时间转换为 UTC 毫秒 → Gateway `PUT /api/v1/agent/runs/:run_id/draft/deadline` → Agent 经 User/IM 验证本人及当前范围 → 短事务比较版本并保存时间 → 页面审查保存结果 → Confirm 同时比较时间/版本/文字/负责人 → 冻结内容和 Task 键 → Task 创建 → Agent 保存任务 ID。超时后本人重读、显式重试，原毫秒值和 Task 键不变。时间解释不进入 Gateway，Task 数据仍归 Task 服务。

版本相同且时间不变为 no-op；变化才加一。0 明确清空。非零时间缺审查值的旧确认拒绝，零值兼容缺字段；新页面总发送审查值。旧响应缺时间仍可读但禁止写入，不假定为零。页面使用已有 Date/Intl，新增时间输入遇到上海历史夏令时不存在/重复的时刻拒绝；未改动的精确旧 UTC 值直接沿用，不重算。代价为非零旧确认需升级、输入解析规则需维护，未新增前端依赖。

### 全部实际修改文件

| 文件 | 作用 |
| --- | --- |
| [api/README.md](../api/README.md) | HTTP、兼容及审查说明 |
| [api/agent_draft.go](../api/agent_draft.go) | 严格时间字段解析、共享输出范围检查 |
| [api/agent_draft_confirm.go](../api/agent_draft_confirm.go) | 确认审查时间转发和结果比较 |
| [api/agent_draft_confirm_deadline_test.go](../api/agent_draft_confirm_deadline_test.go) | 缺失/零/异常确认测试 |
| [api/agent_draft_deadline.go](../api/agent_draft_deadline.go) | 专用时间保存处理器 |
| [api/agent_draft_deadline_test.go](../api/agent_draft_deadline_test.go) | 请求、版本、返回值及映射测试 |
| [api/deadline_flow_integration_test.go](../api/deadline_flow_integration_test.go) | HTTP/TCP gRPC Agent 组合验证 |
| [api/main.go](../api/main.go) | 15 秒预算 PUT 路由 |
| [cmd/agent/confirmation_test.go](../cmd/agent/confirmation_test.go) | 现有非零冻结时间明确审查，保留原断言 |
| [docs/agent-assignee-design.md](agent-assignee-design.md) | 记录上一批已合 main 的历史状态 |
| [docs/agent-deadline-design.md](agent-deadline-design.md) | 三项选型、分步交付及完整审查清单 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A51—A53 确认及普通实现取舍 |
| [docs/deadline-collaboration-contract.md](deadline-collaboration-contract.md) | 三执行分支共同契约 |
| [docs/project-plan.md](project-plan.md) | 实际进度与下一项 |
| [docs/stage6-acceptance.md](stage6-acceptance.md) | 本地验证及待真实验收项 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | 分支、交付、耗时与集成记录 |
| [examples/chat.html](../examples/chat.html) | 上海时间编辑/保存/审查与上下文保护 |
| [examples/chat.test.cjs](../examples/chat.test.cjs) | 115 项回归，其中新增 15 项 |
| [rpc/agent/README.md](../rpc/agent/README.md) | RPC、冻结及兼容说明 |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 专用编辑 RPC 和 optional 确认审查字段 |
| [rpc/agent/draft_access.go](../rpc/agent/draft_access.go) | 访问链新增时间写入器 |
| [rpc/agent/draft_assignee_confirm_test.go](../rpc/agent/draft_assignee_confirm_test.go) | 既有非零时间确认适配 |
| [rpc/agent/draft_confirm_rpc.go](../rpc/agent/draft_confirm_rpc.go) | 编排审查值核对及传递 |
| [rpc/agent/draft_confirm_rpc_test.go](../rpc/agent/draft_confirm_rpc_test.go) | 确认函数签名回归适配 |
| [rpc/agent/draft_confirm_store.go](../rpc/agent/draft_confirm_store.go) | 冻结事务内再次核对审查时间 |
| [rpc/agent/draft_confirm_store_test.go](../rpc/agent/draft_confirm_store_test.go) | 事务确认调用适配与原断言保留 |
| [rpc/agent/draft_deadline_rpc.go](../rpc/agent/draft_deadline_rpc.go) | 本人/范围/版本/状态验证与编辑 RPC |
| [rpc/agent/draft_deadline_store.go](../rpc/agent/draft_deadline_store.go) | 锁定完整行，只改时间和版本 |
| [rpc/agent/draft_deadline_test.go](../rpc/agent/draft_deadline_test.go) | 时间写入与确认/重试业务边界测试 |
| [rpc/agent/draft_revision_test.go](../rpc/agent/draft_revision_test.go) | 原版本测试调用适配 |
| [rpc/agent/draft_rpc.go](../rpc/agent/draft_rpc.go) | 数据库写入器接线 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 正确 pb 目录生成协议结构 |
| [rpc/agent/pb/agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) | 正确 pb 目录生成调用接口 |
| [rpc/agent/task_draft.go](../rpc/agent/task_draft.go) | 统一确认时间审查规则 |

### 实际验证与未验证部分

- `go test ./... -count=1` 全量通过；包括新增组合测试和现有负责人、确认、回帖、IM/Task 回归。
- `node --test examples/chat.test.cjs` 115/115 通过；新增 15 项覆盖上海时区转换、非上海设备、秒/毫秒、清空/no-op、历史夏令时、旧响应只读、冲突与上下文、版本上限及冻结时间不变。
- Linux Agent/Gateway 编译通过；`git diff --check` 通过。
- 组合测试使用实际 HTTP/TCP gRPC 和生产 Agent/存储适配器；SQL、User/IM/Task 为替身。测试从保存毫秒值、拒绝旧版本/未审查确认、Task 首次响应超时到原键重试成功，并验证已成功后权限撤销仍拒绝。

未运行真实 MySQL、生产服务进程、浏览器、容器或方舟模型；没有执行真实迁移、推送或云部署。人工编辑通过不表示模型已能识别截止时间；自动提取依据/有限解析和首次指令参考时刻仍待下一批。阶段 6 还包括后续多项草稿与群内触发，阶段 7 仍需整体真实验收。
