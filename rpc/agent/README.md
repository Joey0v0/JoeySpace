# Agent 只读问答基础（阶段 5）

当前已有 `ContextReader`、同步 `Agent.Ask` 的 proto/处理入口、`GroupAnswerer`、Eino 生成器、方舟 ChatModel 构造、按需调用的任务只读工具，以及独立 Agent RPC 的本地和 Compose 启动配置。Gateway 已有显式问答 HTTP 入口和原生演示页操作；尚无真实浏览器、容器或模型调用验证。`Ask` 接收 `team_id`、`group_id`、`question`，从 gRPC `authorization: Bearer <Token>` metadata 取原登录 Token，验证参数并设置 20 秒总处理超时，再交给 `Answerer`。处理入口只检查 Token 格式，不自行验签或判定群权限；未配置答复器时返回 `Unavailable`，不会生成占位答案。

群问答先走 `Ask → GroupAnswerer → ContextReader.GroupMessages → IM ListTeamGroupMessages`；IM 用原 Token 核对当前团队与群成员资格、群归属。成功后生成器把文本消息恢复为时间正序，以 JSON 数据放入 User 消息；System 消息包含固定的只读与不编造规则。图片、文件内容不传入模型。没有配置任务 RPC 时，沿用 Eino Chain 只读群问答；配置后，生成器把当前请求绑定的 `list_team_tasks` 交给 ChatModel，模型可以直接回答，或请求一次任务工具。后者经 Eino ToolNode 调用 `ContextReader.TeamTasks → Task ListTeamTasks`，再把工具结果交给模型生成最终答复。工具失败或任务服务拒绝访问时直接返回错误，不继续调用模型；同次问答最多执行一轮、一个工具调用，重复或未知工具请求被拒绝。生成器不接收 Token；Token 与团队 ID 仅保存在后端构造的工具中。代码区分系统指令与群消息/工具结果数据，但不能单凭提示词保证真实模型完全抵御资料中的诱导内容。

`ContextReader` 为后续两个只读工具提供受限的数据入口：

- `GroupMessages` 带原用户 Token 调用 IM 的 `ListTeamGroupMessages`，读取指定团队群最新的最多 20 条消息。IM 负责核对当前团队及群成员资格。
- `TeamTasks` 带原用户 Token 调用任务服务的 `ListTeamTasks`，按现有任务 ID 升序读取当前团队最多 20 条任务。任务服务负责核对团队成员资格。

`NewListTeamTasksTool` 将 `TeamTasks` 包成 Eino 的 `list_team_tasks` 只读工具。工具创建时由后端固定原用户 Token 和团队 ID，模型看到的工具 schema 不含这两个字段；即使传入额外的 `team_id` 参数，也不会改变 RPC 查询范围。工具结果只包含任务字段，整数 ID 转为十进制字符串。本地假模型与 RPC 替身已验证按需调用、工具结果回传、身份范围、权限失败和重复工具请求；真实模型是否正确选择工具仍待验收。

两个读取方法各自通过业务 RPC 授权，单次调用最多等待 5 秒，不直接查询 IM 或任务表。当前 Ask 必须先成功读取群历史；仅当模型请求任务工具时才访问任务服务。群消息内容是待分析数据，不能把它当作权限指令。Eino 核心固定为 `v0.9.13`，方舟适配器固定为 `v0.1.71`。`NewArkGroupReplyGenerator` 仅在 `ARK_API_KEY` 与 `ARK_MODEL_ID` 均有值时构造 ChatModel；这两个环境变量不写入仓库。方舟单次请求超时 15 秒、输出最多 1024 Token、适配器自动重试次数为 0；外层 Ask 总超时仍为 20 秒。构造不会发送模型请求，只有授权读取群消息后的 `Generate` 才会调用模型。当前测试以本地假 ChatModel 验证 Ask→IM→Eino Chain/ToolNode→Task，并使用占位凭证验证方舟构造；接入点 ID 与预算尚未确定，未请求真实模型，也未连接真实业务服务或数据库。输出上限与超时是真实调用前的初始限制，实际效果待联调核对。

本地进程由 `cmd/agent` 启动，默认读 `rpc/agent/etc/agent.yaml`，仅监听 `127.0.0.1:9004`。启动前需设置 `IM_RPC_ADDR`、`TASK_RPC_ADDR`、`USER_RPC_ADDR`、`AGENT_MYSQL_DSN`、`ARK_API_KEY` 和 `ARK_MODEL_ID`；缺地址、数据库配置或模型凭证时会在监听前失败，日志不输出密钥或 DSN。启动时会连接 Agent 草稿数据库，并创建 gRPC 客户端和 ChatModel；不请求模型。后端服务是否可达、账号权限和模型可用性仍须实际调用验证。占位凭证可用于本地构造测试，但不能用于真实问答。Compose 中 `agent-rpc` 使用 `agent` profile，容器监听 9004 且不向宿主机发布端口；只有具备实际模型接入点与预算时才应显式启用。Dockerfile/Compose 目前仅做静态检查，没有构建或运行容器。

`api/agent_flow_integration_test.go` 另以独立子进程运行 Agent RPC，并用本地 TCP gRPC 替身提供 IM/Task。测试通过真实 HTTP 与 gRPC 传输验证授权群历史、一次 Eino 任务工具调用、原 Token 和大整数 ID 传递；IM 拒绝访问或任务服务故障时不返回答案。这个测试不启动生产 `cmd/agent`，也不调用方舟或真实数据库。

验证：在仓库根目录运行 `go test ./api ./cmd/agent ./rpc/agent`。

## 阶段 6：单项任务草稿读取

`task_draft.go` 定义 Agent 内部单项草稿与运行范围：团队、群和发起人 ID 由可信服务提供；任务字段先按创建范围校验。初始 `waiting_confirmation` 允许发起人编辑；确认冻结后进入 `creating`，持久保存任务 ID 后进入 `succeeded`。只有发起人可操作，每次重新核对当前团队群资格，创建时仍由 Task RPC 复核负责人及来源消息。Agent 已接入生成、读取、编辑与同步确认 RPC，Gateway/页面确认入口已接入。决策见[架构记录](../../docs/architecture-decisions.md) A35/A36/A41。

`draft_store.go` 已按用户选择的独立两表方案提供存储方法：服务端生成运行 ID 和可信运行范围，事务写入 `agent_runs` 与第 0 项 `agent_task_drafts`；按运行 ID 与发起人 ID 共同查询，其他用户与不存在的运行都返回 `NotFound`。SQL 替身测试覆盖事务回滚、重复 ID、隔离读取、请求去重及故障屏蔽。新库定义见[初始化 SQL](../../deploy/mysql/init.sql)，已有库需在启用阶段 6 草稿功能前核对并依次执行[010 建表](../../deploy/mysql/migrations/010_agent_runs_and_drafts.sql)和[011 去重](../../deploy/mysql/migrations/011_agent_run_request_key.sql)。Agent 进程现已配置数据库连接与草稿生成入口；这不是实际运行过的 MySQL 持久化。建表取舍见[架构记录](../../docs/architecture-decisions.md) A37/A39。

`GetTaskDraft(run_id)` 通过 `authorization: Bearer <Token>` 传入原 Token。`draft_access.go` 依次调 User `GetMyInfo`、按运行 ID 和发起人过滤 Agent 草稿、调 IM `CheckTeamGroupAccess` 核对当前团队群范围；任一步失败不返回草稿。RPC 响应含运行状态、草稿和 `task_id`（尚未持久记录成功时为 0）；请求不能指定身份或团队群作为授权依据。Gateway/页面已有生成、读取、文字编辑和确认，任务 ID 经 HTTP 用字符串返回。本机 gRPC 与 SQL 替身测试通过，未连接实际 MySQL。见[架构记录](../../docs/architecture-decisions.md) A36/A38/A41。

`PrepareTaskDraft(team_id, group_id, instruction)` 是显式同步生成入口：请求须带原登录 Token 和 1～64 位 `idempotency-key`；Agent 从 User 确认发起人，经 IM 读取授权团队群最近最多 20 条消息。相同发起人、键与规范化指令/范围复用原运行 ID，不再次调用模型；新请求用 Eino/方舟生成严格 JSON，只接受标题、说明和本次文本消息的来源 ID。模型输入输出 ID 用十进制字符串；负责人和截止时间暂留空。Agent 独立 Snowflake 节点默认 5，事务保存运行与第 0 项草稿。响应丢失须复用生成键；并发请求可能都调用模型，但唯一约束只保存一份草稿。真实方舟与 MySQL 未验收；Gateway/页面已有生成、读取和文字编辑，确认现已接入 Agent RPC、Gateway 和页面。选型见[架构记录](../../docs/architecture-decisions.md) A39。

草稿编辑的服务内部链路已实现第一小步：`draftAccessReader.editText` 复用 Token 身份、发起人过滤及当前团队群资格校验，只允许在 `waiting_confirmation` 状态修改标题和说明；负责人、截止时间与来源消息保持原值。`draftStore.updateDraftText` 在事务中锁定运行与第 0 项草稿，比对读取时的旧文本及当前状态后写入；若其他编辑已改变文本则拒绝覆盖。Agent/SQL 替身测试覆盖授权、离队、无效标题、状态变化、过期文本及写入失败。该内部小步当时尚无编辑 RPC；现已接入 Agent gRPC，Gateway 和原生页面也已接入标题/说明编辑，真实 MySQL 未验收。沿用[架构记录](../../docs/architecture-decisions.md) A36/A37 的发起人权限和独立草稿表方案。

`EditTaskDraft(run_id, expected_title, expected_description, title, description, expected_revision)` 已接入 Agent gRPC：调用方携带原 Token、最近一次读取取得的旧文本和正版本号；Agent 重新核对发起人及团队群资格，只在待确认状态修改标题、说明，成功返回与 `GetTaskDraft` 相同形状的完整草稿。旧版本或旧文本与当前草稿不同、或读取后发生并发修改时返回 `Aborted`，调用方应重新读取并审查后再提交；负责人、截止时间和来源消息不由该方法改变。真实 gRPC 编解码与本地 User/IM/存储替身测试覆盖成功、无 Token、越权、离队、过期表单、已完成状态和非法标题。Gateway 已提供 PUT 草稿文字编辑入口，详见 [HTTP 说明](../../api/README.md)；原生页面编辑和冲突重读已有 Node 测试，真实浏览器及 MySQL 验收仍待完成。沿用 A36/A37 的既定权限和数据归属；原旧值校验见 A40；用户已确认 A50 版本方案，旧值仍作为额外核对，不能替代版本。

## 单项草稿同步确认

`ConfirmTaskDraft(run_id, expected_title, expected_description, expected_revision, optional expected_assignee_id)` 接收本人原 Token、最近读取的已保存文字及负责人审查值，不接受客户端创建键。含称呼或人工选择的草稿必须明确提供负责人 ID，零表示已审查未指派；未处理歧义不能直接确认。Agent 重新核对身份与团队群资格，待确认的正负责人经 User 复核后，短事务锁定运行/草稿、比较版本、文字及负责人，保存 `agent-task-{run_id}-0` 并进入 `creating`；事务提交后以冻结字段、原 Token、固定键调用 Task `CreateTask`。Task 返回正任务 ID 后，短事务保存 ID 并进入 `succeeded`，随后才返回成功。创建阶段保留 12 秒上限，Task 调用最多 5 秒；启用回帖后总预算 18 秒，回帖阶段最多 5 秒，其中 IM 调用最多 4 秒。Gateway 确认路由 19 秒，Agent 服务端 20 秒、Gateway 的 Agent RPC 客户端 21 秒，实际仍受调用方更短截止时间限制。

创建错误不会解冻草稿或标记“没有创建”。调用方须重读状态，本人用同一运行和已保存文字显式重试；Task 的请求键及内容指纹复用原任务。已成功的重复确认仍核对当前权限，然后返回原结果和回帖状态，不自动重发。没有后台自动恢复；发起人失去权限后不能由别人接手，未确定结果需后续核对流程。详情与取舍见[确认设计](../../docs/agent-confirmation-design.md)和 A41。

更新后的草稿读取也依赖新增字段，已有库须在启动前按顺序核对并执行尚未执行的 [010](../../deploy/mysql/migrations/010_agent_runs_and_drafts.sql)、[011](../../deploy/mysql/migrations/011_agent_run_request_key.sql)、[012](../../deploy/mysql/migrations/012_agent_draft_task_result.sql)。测试仅使用 SQL/业务替身、本机 gRPC 与重构 Agent 实例，不代表真实 MySQL、Task、进程崩溃恢复或容器验收。

## 独立机器人回帖（2026-10-03）

按已确认 A44/A45/A46 接入。`bot_client.go` 创建独立 IMBot mTLS 客户端，五项配置全空时关闭回帖；部分配置或证书加载失败拒绝启动，无明文回退。配置名、证书目录及可选 Compose 覆盖见[部署说明](../../deploy/README.md)。启动构造不请求模型；实际 Agent 启动仍要求既有方舟配置，本轮没有准备真实接入点或请求模型。

首次确认保存任务成功结果后，同步尝试回帖：短事务重新锁定成功运行/草稿，核对持久任务 ID 与冻结内容，从结果构造版本 1 卡片，再存入 Agent 自有 `agent_task_replies`。消息 ID 固定为 `bot-task:<run_id>`，提交事务后才以原 Token 调 IMBot；IM 仍另查当前群权限。IM 返回匹配消息 ID 的 `accepted=true` 后，Agent 只把受理状态向成功推进。两侧记录不是跨服务事务；响应丢失或 Agent 保存受理失败时仍保留原意图，可能重复提交相同消息，客户端继续按 `msg_id` 去重。启用前完成 Agent 的 [015 迁移](../../deploy/mysql/migrations/015_agent_task_replies.sql)及 IM 的 013/014、机器人资料、读写链路和证书准备。

`GetTaskDraft` 读取回帖状态不发送消息。`RetryTaskReply(GetTaskDraftRequest{run_id})` 是本人显式操作，重新核对 Token、发起人及当前团队群资格，只发送已成功任务的原回帖，绝不调用 Task 或再次请求模型。仅传运行 ID，不接受卡片内容、任务 ID、机器人 ID 或范围。已受理的重试仍校验当前资格，再返回持久结果。调用方需重读后审查再重试；Gateway 已接入 `POST /api/v1/agent/runs/{run_id}/reply/retry`，原生页面只在成功重读原运行后开放可重试状态，失败再次要求重读。见 [HTTP/页面说明](../../api/README.md#回帖状态与显式重试2026-10-03)。

追加响应字段 `reply_status` / `reply_msg_id`，旧字段号保持兼容：`disabled` 表示功能关闭；`not_started` 表示未形成持久回帖；`pending` 表示已保存意图但未在 Agent 保存受理结果，可能已经被 IM 受理；`accepted` 只表示 IM 已受理，不表示历史已保存或成员已收到。`unknown` 仅在任务成功后的回帖准备失败时返回，保留任务成功，需重读核对；读取存储失败会报错，不假报未开始或已受理。Gateway 已追加透出并校验这些字段，旧 Agent 省略两字段时兼容但不开放页面回帖重试。

SQL/业务替身与本机 gRPC 测试覆盖响应丢失、受理保存失败、重构服务后固定内容重试、无 Token/非本人/离队拒绝、非法 IM 结果以及任务成功保留。临时证书测试经 Agent 重试 RPC 调实际 mTLS IMBot 客户端，验证对端名称和 Agent 身份；IM 业务实现、数据库及 Kafka 均未在这条测试链使用真实环境。完整文件与验证范围见[审查记录](../../docs/agent-group-reply-design.md#agent-回帖接线与中断收尾2026-10-03)。

## 草稿负责人提取与解析（2026-10-03）

草稿生成模型 JSON 现在必须含 `assignee_name` 字符串；没有分工信息填空字符串，不能省略或填 null。模型只能提取用户指令/已授权文本中出现的称呼，不能给成员 ID、解析状态或推断代词对应的真实姓名。Agent 检查称呼确实出现在输入文本中，随后以原 Token 调 User 的 `ResolveTeamMember`；这项文本检查不代表模型理解分工一定正确，仍需本人审查。

草稿新增 `assignee_name` 与 `assignee_resolution`，GET RPC 透出字段。状态为 `none`（无称呼）、`matched`（唯一匹配并保存候选 ID）、`not_found`、`ambiguous`、`truncated`。只有完整唯一匹配保存非零 ID；失败/异常响应拒绝生成，不当成未指派。解析调用复用 5 秒读取上限，并受原 20 秒生成总预算限制；不新增自动重试。候选名单不存入 Agent 表，后续选择须重新查当前成员。

更新 Agent 前已有库须执行一次 [016 迁移](../../deploy/mysql/migrations/016_agent_draft_assignee.sql)，因为草稿读取、锁定和新增写入均使用新列。旧行默认两列为空，保留原负责人、请求键与结果；新库初始化已同步。幂等重试仍先检查原运行，不重新请求模型、解析姓名或改写原草稿。

含称呼草稿的旧确认保护保留：缺少 `expected_assignee_id` 仍返回 `FailedPrecondition`，不能只携带新版本绕过审查。当前接口允许本人审查唯一 `matched`，或先处理未匹配/重名/截断后确认；详情见下方负责人选择。旧草稿与新 `none` 草稿兼容未提供负责人审查值的旧请求；新版页面始终明确提供，包括零。

Agent/进程/Gateway 定向、全量 Go、Linux Agent 编译通过；本机 TCP 测试使用实际生产构造/Eino 链/数据库适配，User、IM、Task 业务和模型、SQL 数据为替身。未执行真实 MySQL、方舟或浏览器联调。全部文件、三步调用链和取舍见[本轮审查记录](../../docs/agent-assignee-design.md#8-agent-提取解析与持久化审查2026-10-03)。

## 草稿内容版本（A50，2026-10-03）

`TaskDraftItem.revision` 是正 int64；旧行经 [017 迁移](../../deploy/mysql/migrations/017_agent_draft_revision.sql)从 1 开始，新草稿同样从 1 开始。更新 Agent 前须在 016 后执行尚未执行的 017，读取旧草稿也依赖 revision 列。文字实际改变在原锁定事务内递增一次；无变化保存不递增，版本耗尽拒绝编辑。

Edit/Select/Confirm RPC 必须携带 `expected_revision`；缺失或非正值返回 InvalidArgument，版本不匹配返回 Aborted，要求本人重读。检查本人当前资格后仍在事务内比较，避免读取与写入间的并发变更。冻结、任务成功、回帖受理不递增内容版本；超时后本人读取原版本并沿用原任务键重试。版本不替代权限、负责人审查或 Task 幂等键。

[选项、兼容与完整审查](../../docs/agent-assignee-design.md#10-草稿版本基础与现有写入接线审查2026-10-03)。本地 Go/页面替身与编译通过，不代表真实迁移或完整生产链验收。

## 本人选择负责人与确认审查（2026-10-03）

`SelectTaskDraftAssignee(run_id, optional assignee_id, expected_revision)` 只允许原发起人操作待确认草稿。负责人字段必须存在，正 ID 经原 Token 调 User `CheckTeamMemberByID`，零表示本人明确未指派；目录展示不能替代这个资格检查。数据库事务内再次比较范围、版本和完整草稿，正 ID 保存 `selected`，零保存 `unassigned`，保留最初提取的称呼。ID/选择状态实际改变才递增版本，相同选择不递增；文字编辑保留选择，冻结后不能再改。

确认在编排和锁定事务内都比较本人审查的 ID，并拒绝未处理的 `not_found/ambiguous/truncated`。待确认阶段复核目标成员；`creating/succeeded` 的重试不再解析或改选冻结负责人，也不因目标后来离队否定已保存结果。仍检查发起人当前群资格，Task 用原创建键继续其幂等规则；没有自动后台重试。HTTP 与页面按[共同契约](../../docs/assignee-collaboration-contract.md)接线；本批验证范围和所有修改文件见[审查记录](../../docs/agent-assignee-design.md#11-负责人选择闭环与并行集成审查2026-10-03)。现有 016/017 列足够，本批没有新增迁移。

## 本人编辑截止时间与确认审查（2026-10-03）

`EditTaskDraftDeadline(run_id, optional due_at_unix_ms, expected_revision)` 接原 Token，截止字段必须存在；0 明确清除，正 UTC 毫秒最大253402300799999。复用本人/当前团队群校验，只能修改待确认草稿；事务锁定完整草稿并再次比较范围、内容及版本，只更新时间。实际变化版本加一，相同值 no-op，耗尽版本不能修改，其他文字/负责人/来源保持。

Confirm 追加 optional `expected_due_at_unix_ms`。非零保存值必须显式审查；零旧草稿兼容缺失，新页面总是发送包括0。编排与冻结事务均核对，错值Aborted、非零缺失FailedPrecondition、非法请求值InvalidArgument；冻结后的任务与回帖重试不重算或改变截止时间。实际冻结UTC值仍交现有 Task 创建与幂等摘要，不新增表/迁移或依赖。

用户已选 A51/A52/A53：上海解释时区、模型提取原文由 Go 有限解释、页面固定首次指令参考。人工批次只接编辑/审查，之后的请求和解析前置见下节，不能据此宣称 Agent 已理解“明天下午”。[人工方案与审查](../../docs/agent-deadline-design.md#7-人工截止时间闭环审查)、[HTTP/页面](../../api/README.md#草稿截止时间编辑与确认审查2026-10-03)。

## 固定指令参考请求与有限解析前置（2026-10-03）

`PrepareTaskDraftRequest`新增optional `instruction_reference_unix_ms`（字段4），存在须合法正UTC毫秒；缺失保留旧fingerprint JSON字节。新参考与团队/群/指令加入摘要，同键换参考冲突；查同键前照常核对当前用户/授权群，重放不调用模型。设备参考只作将来的时间解释输入，不能替代服务端created_at或权限。

`interpretDraftDeadline`是本包内部纯Go解释器，接原文、指令/消息来源和固定参考。消息来源必须唯一且属于传入授权文本，按那条消息的发送时间；指令相对日期必须有参考，绝对日期可无参考。明确完整日期或今天/明天/后天带时分可解释，支持秒毫秒；模糊/错误日期/DST歧义输出needs_input，不擅补，非法证据拒绝。依赖现有Asia/Shanghai标准时区数据，加载失败返回Unavailable，不固定+08或读取Now。

解释器尚未接Eino输出、存储或对外RPC；原模型仍只生成旧四字段，运行摘要不是可读解析依据。下一批需完整持久元数据/状态及本人歧义处理与确认保护。本批没有新增迁移；[共同契约](../../docs/deadline-reference-contract.md)、[21文件及验证范围](../../docs/deadline-reference-review.md)。
