# Agent 任务负责人匹配方案

日期：2026-10-03。状态：**负责人选择、确认审查、Gateway 与页面闭环已在集成分支通过本地验证；main 合入待用户审查，真实环境未验收**。A48/A49 按推荐 A/A 推进、待逐项复核：上轮给出两个 A 后，用户回复“继续进行下一步”，未记为逐项明确选择；A50 版本方案由用户明确选择。第 6—10 节为此前各步历史，第 11 节是本批最新九步交付、全部文件和验证范围。

## 1. 要解决的问题与现有能力

目标示例：群讨论中有“张三负责整理接口文档”，Agent 生成任务草稿时能提示真实负责人，由发起人审查后创建任务。模型不能自行编造成员 ID，也不能用一个姓名越过团队权限。

方案提出时已核对的现状（本轮新增 User 接口见第 7 节）：

- User 拥有用户和团队成员资料，已有分页 `ListTeamMembers`（每页最多 100）和 `CheckTeamMemberByID`。前者返回用户名、昵称和角色，后者检查调用者及目标成员当前资格，并拒绝停用账号。
- Task 支持数字 `assignee_id`，非零时经 User 校验当前可用团队成员；零代表未指派。负责人范围是团队，不要求属于任务来源群。
- Agent 草稿虽有 `assignee_id`，当前模型输出只含标题、说明和来源消息，生成入口拒绝模型直接给出非零负责人。草稿编辑仅支持标题/说明，确认也只比较这两项。
- 现有数据库使用 `utf8mb4_unicode_ci`；直接使用普通 SQL 文本相等比较，不能作为严格区分大小写、重音的姓名匹配承诺。

代码依据：[成员目录](../rpc/user/member_list.go)、[成员资格](../rpc/user/team_member_target.go)、[任务创建](../rpc/task/create.go)、[生成入口](../rpc/agent/draft_preparer.go)、[模型输出](../rpc/agent/eino_task_draft.go)、[草稿契约](../rpc/agent/agent.proto)、[确认冻结](../rpc/agent/draft_confirm_store.go)。

## 2. A48：在哪里匹配、匹配谁

| 候选方案 | 优点 | 代价与限制 |
| --- | --- | --- |
| A：User 增加专用团队成员姓名解析 RPC（推荐） | 身份规则留在资料所属服务；只返回相关候选；Agent 不读取 User 表 | 增加 protobuf 方法、查询与测试；严格匹配需显式处理数据库比较规则 |
| B：Agent 翻页读取已有成员目录，再自行匹配 | 可复用现有 RPC | 需完整翻页才能排除重名；传输更多成员资料，匹配规则分散在 Agent |

建议的 A 契约：调用者携带原 Token、团队 ID 和一个明确姓名，在 User 核对调用者当前团队资格后，按用户名或昵称查找该团队当前启用成员。只访问 User 自有数据，不要求目标在来源群；与既定 Task 负责人范围一致。来源群访问权仍由 Agent 经 IM 单独校验。

第一版建议仅去除输入首尾空白，然后对存储的完整用户名/昵称严格匹配，区分大小写和重音，不做拼音、包含、别名或语义猜测；具体查询必须验证不会受默认数据库排序规则影响。同一成员同时匹配用户名和昵称只计一次，用户名命中不优先于其他成员的同名昵称。真实姓名尚无独立字段，因此这里的“姓名”实际指用户名或昵称。

建议每次最多返回 20 个候选，额外检测是否截断，响应明确区分零个、唯一、多个和结果不完整。结果不完整不能认定唯一，也不能默认选第一位；可请发起人输入更准确的用户名。查询失败也不能伪装成“未找到”。本轮按推荐实现 User 端边界，用户逐项选择仍待复核。

## 3. A49：谁决定最终负责人

| 候选方案 | 行为 | 取舍 |
| --- | --- | --- |
| A：唯一严格匹配预填，歧义由本人选择（推荐） | 唯一候选显示在草稿；重名、未匹配或结果不完整时，必须选择真实成员或明确选择未指派后再确认 | 保留 Agent 提取价值；需保存解析状态、扩展编辑和确认契约及页面 |
| B：负责人始终手动选择 | 模型不提取负责人，页面由本人选择成员 | 改动和规则较少，但“张三来做”仍需人工处理 |
| C：模糊匹配后由模型自动选人 | 用上下文、近似姓名推断负责人 | 操作少，但可能把任务交给错误成员；首版不推荐 |

推荐 A 的具体规则：

1. 模型只提取文字中的负责人称呼，不提供成员 ID；后端调用 User 获取真实候选。原称呼留在 Agent 草稿中，便于发起人核对。没有负责人信息时沿用未指派。
2. 唯一严格匹配可以预填，仍须本人明确确认整份草稿后创建；两位“张三”不能因列表顺序选中第一位。
3. 明确提到了负责人却未解析成功时，不能悄悄变成未指派。本人必须处理歧义，或明确决定不指派。“他”“老王”等无法凭严格用户名/昵称确定的称呼不猜 ID；第一版也不自动推断“我”对应谁。
4. 手动选择的成员仍须经 User 检查。只有草稿发起人、且保有当前团队群资格，能修改或确认；不增加协作审批或代替他人确认。
5. 确认时沿用 Task 对当前成员资格的检查。成员离队/停用导致新任务不能指派时，应提示重新处理，不静默换人。已经创建的任务重试复用原结果；其幂等行为保持不变。

## 4. 对整体架构的影响与必须保留的约束

预期调用链：本人请求 → Gateway → Agent 校验身份与来源群 → Eino 提取负责人称呼 → User 解析候选 → Agent 保存待确认草稿 → 本人审查/选择 → Agent 冻结整份创建内容 → Task 创建 → 原有 IM 回帖链。

User 继续拥有真实身份和成员关系；Agent 拥有提取称呼、解析状态和本人选择；Task 拥有创建后的任务。沿用原生页面、gRPC 与现有数据库，不增加语言、中间件、服务或后台执行身份。

`assignee_id = 0` 无法区分“没有提到负责人”“尚未解析”“本人明确选择未指派”，需要 Agent 自有的状态字段及增量迁移。具体字段和 RPC 在方案确认后按小步设计，不能把整个团队目录长期复制到 Agent。

负责人变为可编辑后，必须扩大现有编辑/确认的并发比较范围：用户看到的旧负责人或解析状态发生变化时，旧页面不能确认新版草稿。比较旧字段或使用版本号的具体实施取舍需记录；本轮未选择或实现。冻结进入 creating 后不得修改负责人，所有任务重试和回帖继续使用同一冻结内容、操作键与实际任务 ID。

旧草稿、旧客户端的兼容也须明确：既有未指派草稿仍可读取；涉及新负责人状态的草稿，不得由缺少审查字段的旧客户端直接确认。权限校验失败、解析依赖失败与未匹配需要分别表达。

## 5. 确认后的实施顺序与验收

以下是分步顺序，实际完成部分见第 7 节：

1. 先做 User 专用解析 RPC 和定向测试：权限、停用成员、同名、用户名/昵称交叉同名、截断、数据库失败及严格字符比较。先验证成员解析，不同时改页面。
2. 再接 Agent 提取、解析和持久草稿状态，补旧草稿兼容；使用本地模型/数据库替身，不请求真实模型。
3. 补本人选择、编辑冲突与确认冻结，再接 Gateway/原生页面；验证成员退出、不确定重试和已有回帖幂等不受影响。

每步实施前重新说明具体修改文件。真实 MySQL 的字符比较/迁移、方舟模型提取和浏览器操作属于最终环境验收，不能仅凭替身测试标为完成。

## 6. 方案准备审查清单（上一轮）

上一轮仅修改三个文档：

- [负责人设计](agent-assignee-design.md)：现状、两个待选方案、调用链、影响和后续验证目标。
- [架构记录](architecture-decisions.md)：A48/A49 标为待决定，所选方案尚无。
- [项目计划](project-plan.md)：关联本方案并标明负责人匹配尚未实现。

上一轮只核对代码与文档引用，不运行 Go/页面测试，也不声称负责人功能已完成；当时等待用户选择后再推进。

## 7. User 解析接口实现与审查（2026-10-03）

本轮只有一个业务目标：将严格姓名匹配做成可验证的 User RPC，供后续 Agent 调用。不是完整负责人指派流程；没有修改模型、Agent 草稿、页面、依赖版本或数据库结构。

确认来源：上轮向用户推荐 A48/A49 两项 A，用户回复“继续进行下一步”，本轮事先说明据此按推荐推进。架构记录标为“按推荐方案推进、待逐项复核”，不把继续指令记为逐项明确选 A。

实际调用链：RPC `ResolveTeamMember` → 复用 `CheckTeamMember` → 原 Token 经 `GetMyInfo` 校验当前启用账号 → 核对目标团队资格 → User 联查自己的团队成员与启用用户 → 返回有上限的候选及截断标记。请求不接收调用者 ID，也不接收来源群；查询不访问 IM、Task 或 Agent 表。

普通实现取舍（在 A48 的已讨论范围内）：

- 用单次带 OR 的查询匹配两个字段，复用团队成员唯一键，同一人不会因两字段命中出现两次；分别查两次再合并会增加查询与去重逻辑。
- 用显式 `CAST(... AS BINARY)` 做严格字节比较，而不改变用户表排序规则或使用普通文本相等；避免影响既有用户名唯一性。代价是姓名表达式不直接使用文本索引，团队范围与结果上限限制首版读取，但不等于固定扫描成本，真实查询性能待验收。
- 复用 `TeamMember`，响应增加候选列表与 `truncated`，不新增另一套成员模型或解析状态枚举。空结果、唯一、多名、截断可由这两个字段表达，未来 Agent 必须据此区分。
- 查询 21 条、返回最多 20 条，明确告知结果不完整；只取 20 条且无截断信号会掩盖候选。查询失败返回错误，不能伪造空结果。

全部实际修改文件（10 个，不包含 Git 忽略的编译产物）：

| 文件 | 实际变化 |
| --- | --- |
| [rpc/user/user.proto](../rpc/user/user.proto) | 新增解析方法、请求和候选/截断响应 |
| [rpc/user/pb/user.pb.go](../rpc/user/pb/user.pb.go) | 现有工具生成消息结构及描述 |
| [rpc/user/pb/user_grpc.pb.go](../rpc/user/pb/user_grpc.pb.go) | 现有工具生成客户端、服务端与注册代码 |
| [rpc/user/member_resolve.go](../rpc/user/member_resolve.go) | 身份/团队检查、严格启用成员查询、候选上限 |
| [rpc/user/member_resolve_test.go](../rpc/user/member_resolve_test.go) | 权限、输入、候选歧义/上限、绑定参数、失败与真实本机 gRPC 测试 |
| [api/handler_test.go](../api/handler_test.go) | 旧完整 User 客户端测试替身补新增方法，仅返回未实现；Gateway 业务不变 |
| [rpc/user/README.md](../rpc/user/README.md) | 实际 RPC 契约、范围与验证说明 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A48/A49 推进来源、取舍与实际完成范围 |
| [docs/project-plan.md](project-plan.md) | User 子步骤完成，Agent 姓名解析接线仍未完成 |
| [docs/agent-assignee-design.md](agent-assignee-design.md) | 保留前轮设计历史，追加本次完整审查记录 |

验证结果：

- `go test ./rpc/user -run TestResolveTeamMember -count=1` 通过：无效输入/Token、停用调用者、非团队成员拒绝；空/唯一/重名/跨字段候选保留；20/21 人截断；大小写、重音及 SQL 特殊字符原样作为绑定参数；查询失败不误报未匹配；本机 TCP gRPC 响应资料正确。
- `go test ./... -count=1` 最终全量通过。首次回归发现旧 Gateway 测试替身缺新方法，已用四行方法补齐，未改 HTTP 业务行为。
- `GOOS=linux CGO_ENABLED=0 go build -o bin/user-rpc-linux ./rpc/user` 通过。
- 使用本机现有 protoc 31.1、protoc-gen-go v1.36.11、protoc-gen-go-grpc v1.5.1 重新生成；不下载或升级工具、依赖。

未验证部分：SQL 替身核对实际查询条件和参数，但没有执行 MySQL 的排序规则/二进制比较、真实权限数据或性能；候选去重依赖既有团队成员主键。真实数据库、容器、模型、浏览器和云部署仍未联调。Agent 尚未调用此 RPC，唯一预填/歧义处理/本人选择也尚未实现，不将阶段 6 标为完成。

下一小步：先接 Agent 的负责人称呼提取、User 解析调用与草稿状态持久化；涉及字段迁移和旧草稿兼容须提前说明，编辑/确认的并发检查与页面随后分开推进。

## 8. Agent 提取、解析与持久化审查（2026-10-03）

本轮按 A48/A49 已讨论方向推进三个小步骤；未新增服务、语言、中间件、跨服务表访问或后台身份。用户在 User 子步骤完成后回复“进行下一步”。模型及所有业务数据采用本地替身，没有请求真实模型。

### 三个小步骤与调用链

1. **提取称呼**：Eino 原有模型链追加必填 JSON 字符串 `assignee_name`；缺失、null、非字符串及模型直接指定 ID/状态均拒绝。原 Token 身份与授权群上下文检查仍先执行。生成流程还检查称呼出现在当前指令或授权文本中，非文本消息不作证据；这只能限制凭空名字，不能保证模型正确理解分工。
2. **解析与保存**：Agent 以原 Token、团队 ID 和规范化称呼调用 User，验证返回候选的 ID、顺序、严格匹配及上限。完整唯一匹配保存 ID；其他结果保存称呼与明确状态。User 错误、异常响应或配置缺失拒绝生成，不降级为无负责人。Agent 自有草稿表加两列，事务保存运行/草稿与去重依据。重放原请求键返回原运行，不重新生成、匹配或覆盖内容。
3. **读取与确认兼容**：Agent GET RPC 追加两字段，旧行保持空值。含称呼草稿不得通过仅审查标题/说明的旧确认契约；编排调用前、冻结事务内和冻结结果检查均拦截，Task 不被调用。旧草稿及无称呼新草稿保持原行为；已有任务和回帖仍用原冻结内容。

实际链路：本人请求 → Agent 验证 Token → IM 读取授权文本 → 原请求去重 → Eino 提取称呼 → User 解析真实候选 → Agent 事务保存称呼/状态/候选 ID → 本人 GetTaskDraft 时再验证身份和群范围 → 返回草稿。当前 Gateway 未透出新字段，页面未新增负责人交互，因此只标后端子步骤完成。

### 存储状态与旧数据

| 状态 | 保存的称呼与 ID | 当前确认行为 |
| --- | --- | --- |
| 空字符串（旧草稿） | 称呼为空；原 ID 保留 | 沿用旧契约和冻结/重试行为 |
| none | 称呼为空，ID 为 0；跳过解析调用 | 可沿用标题/说明确认 |
| matched | 非空称呼，完整唯一匹配的真实 ID | 暂拒绝旧确认，等待本人审查接口 |
| not_found | 非空称呼，ID 为 0 | 暂拒绝旧确认，等待本人选择或明确未指派 |
| ambiguous | 非空称呼，ID 为 0 | 同上，不能选择第一位 |
| truncated | 非空称呼，ID 为 0；候选不完整 | 同上，不能认定唯一 |

新库初始化及 016 增量迁移一致：`assignee_name VARCHAR(64) NOT NULL DEFAULT ''`、`assignee_resolution VARCHAR(16) NOT NULL DEFAULT ''`。已有库更新 Agent 前必须迁移，读取旧草稿也依赖新列。没有删除、回填或重算旧负责人/任务/请求键；脚本不是重复执行或自动升级机制。

### 本轮普通实现取舍

- **提取后由后端固定调用解析 RPC**，而非让模型选择成员或自由调解析工具：沿用当前单项草稿生成链，执行与范围可验证；代价是首版无多轮自动澄清。
- **在既有 Agent 草稿表增加两个标量字段**，而非新增表或 JSON 成员目录：保留既定数据归属和结构，便于验证状态形状；代价是迁移及旧列缺失时读取失败。只存原称呼、状态、唯一候选 ID，不长期复制 User 的候选名单；后续选择须重新查询当前成员。
- **模型字段必填**，而非缺失时自动认为没有负责人：模型违反新契约会生成失败，避免结构缺失被默认为未指派；旧持久数据仍兼容。
- **检查原文包含提取称呼**，而非信任模型新编姓名：可以阻止输入中不存在的名字；仍无法证明语义正确，须本人审查。
- **先拦截旧确认**，而非让旧客户端忽略新字段后创建：保留已讨论的本人审查要求；代价是本轮含称呼草稿暂不能创建，完整审查/选择与并发比较将在下一步实施。当前未选择版本号或扩展旧值比较，不提前落实该机制。
- 解析复用原 5 秒上下文读取预算，受 20 秒生成总预算限制；无新端口、配置项、自动重试或 Token 持久化。

### 全部实际修改文件

本轮共 27 个文件（包含生成代码、测试、SQL 与五份文档；编译产物不入此清单）：

| 文件 | 实际作用 |
| --- | --- |
| [rpc/agent/eino_task_draft.go](../rpc/agent/eino_task_draft.go) | 模型称呼字段与严格 JSON 解析 |
| [rpc/agent/eino_task_draft_test.go](../rpc/agent/eino_task_draft_test.go) | 新模型契约与非法输出测试 |
| [rpc/agent/task_draft.go](../rpc/agent/task_draft.go) | 状态形状、旧草稿兼容与旧确认限制 |
| [rpc/agent/draft_assignee.go](../rpc/agent/draft_assignee.go) | 原身份解析 RPC、候选验证与状态映射 |
| [rpc/agent/draft_assignee_test.go](../rpc/agent/draft_assignee_test.go) | 唯一/歧义/截断、错误、模型越权及状态形状测试 |
| [rpc/agent/draft_preparer.go](../rpc/agent/draft_preparer.go) | 原文证据检查、解析接线、先去重后生成 |
| [rpc/agent/draft_preparer_test.go](../rpc/agent/draft_preparer_test.go) | 提取内容、权限失败不保存及重放不重查测试 |
| [rpc/agent/draft_prepare_rpc.go](../rpc/agent/draft_prepare_rpc.go) | 生产构造复用现有 User 客户端 |
| [rpc/agent/draft_store.go](../rpc/agent/draft_store.go) | 两列事务写入与读取 |
| [rpc/agent/draft_store_test.go](../rpc/agent/draft_store_test.go) | 状态保存/读取、迁移默认值及旧插入期望 |
| [rpc/agent/draft_confirm_store.go](../rpc/agent/draft_confirm_store.go) | 锁定恢复两字段，事务内禁止旧确认 |
| [rpc/agent/draft_confirm_store_test.go](../rpc/agent/draft_confirm_store_test.go) | 新字段行替身及拦截前无状态变更 |
| [rpc/agent/draft_confirm_rpc.go](../rpc/agent/draft_confirm_rpc.go) | 编排阶段与冻结结果的旧契约检查 |
| [rpc/agent/draft_confirm_rpc_test.go](../rpc/agent/draft_confirm_rpc_test.go) | 旧确认拒绝且不冻结、不调用 Task |
| [rpc/agent/draft_rpc.go](../rpc/agent/draft_rpc.go) | GET/编辑共享响应追加两字段 |
| [rpc/agent/draft_rpc_test.go](../rpc/agent/draft_rpc_test.go) | gRPC 字段传输与访问测试 |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 追加字段号 6/7，不改变旧字段号或方法 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 用既有工具生成消息与描述 |
| [rpc/agent/pb/agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) | 同步重新生成 gRPC 代码 |
| [rpc/agent/draft_assignee_flow_test.go](../rpc/agent/draft_assignee_flow_test.go) | 实际生产构造、本机 TCP、Eino/存储/重放/确认保护组合测试 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | 新库草稿列默认值 |
| [deploy/mysql/migrations/016_agent_draft_assignee.sql](../deploy/mysql/migrations/016_agent_draft_assignee.sql) | 现有库一次性增量迁移 |
| [rpc/agent/README.md](../rpc/agent/README.md) | 实际契约、兼容与当前限制 |
| [deploy/README.md](../deploy/README.md) | 迁移/服务同步要求与未部署说明 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A48/A49 实施进度、备选与代价 |
| [docs/project-plan.md](project-plan.md) | 三个子步骤及下一步进度 |
| [docs/agent-assignee-design.md](agent-assignee-design.md) | 本轮完整审查与历史设计关系 |

### 验证与未验证部分

- `go test ./rpc/agent ./cmd/agent ./api -count=1` 通过；增加组合测试后 `go test ./rpc/agent -count=1` 通过。
- `go test ./... -count=1` 全量通过，包含既有任务确认、回帖重试与 Gateway 回归。
- `GOOS=linux CGO_ENABLED=0 go build -o bin/agent-rpc-linux ./cmd/agent` 通过。
- 新组合测试：本机 TCP gRPC 采用实际 Agent 配置构造、Eino 本地模型与实际 SQL 存储适配，验证保存/读取真实候选 ID 和状态、同键重放模型及解析各仅调用一次、旧确认 Task 调用零次。User/IM/Task 服务业务与数据库数据均是替身，不代表生产完整权限链或真实数据库持久恢复。

没有执行真实 016 迁移、MySQL 字符比较/性能、方舟调用、浏览器或 Compose/云端运行。Gateway 未透出新字段，页面和本人选择尚未实现；不把候选解析当作已确认负责人，也不将阶段 6 标为完成。

后续：版本机制已按用户选择 A50 推进，见第 9、10 节；本人负责人审查与选择接口、相关页面仍待实现，继续保持完整冻结、Task 最终资格校验与原操作键重试。

## 9. A50：负责人编辑与确认的并发检查（用户已定）

日期：2026-10-03。核对现有代码后先提交方案并暂停受影响实现；用户随后在方案问题中明确选择 **A：草稿版本号**。版本基础与现有文字编辑/确认接线已完成，具体审查见第 10 节；本人负责人选择尚未实现。A48/A49 的服务归属与本人处理歧义方向保持不变。本项解决用户审查的内容与实际冻结内容一致的问题，不新增审批人、后台授权或服务。

### 具体问题与候选

例如：你在窗口一看到任务负责人是张三，窗口二把负责人改为李四；窗口一仍点击确认。当前确认只提交标题/说明，不能据此判断负责人已经变化。上一轮暂时拦截含称呼草稿，解除限制前必须覆盖这个问题。

| 方案 | 怎样检查 | 优点 | 代价 |
| --- | --- | --- | --- |
| A：草稿版本号（推荐） | 读取返回版本；每次实际编辑递增；编辑/选择/确认提交上次读取的版本，事务内比较 | 一个字段统一保护全部草稿内容；改动后又改回原值也能识别；以后新增字段不用扩充旧值清单 | 草稿表新增列与一次迁移；所有写入入口和页面需传版本；更新要协调 |
| B：扩展现有旧值比较 | 继续提交旧标题/说明，并加旧负责人 ID、称呼、解析状态；事务内完整比较 | 沿用 A40 当前方式，无需新增版本列 | 请求携带更多旧字段；后续新增可编辑字段也需扩充；值改动后又恢复时不能识别这段变更 |

两种方案都须在取得当前本人权限后，在同一个草稿锁定事务中核对状态与比较依据，再保存编辑或冻结创建意图；比较失败返回冲突，不能覆盖另一窗口的新选择。都不将 User/Task 网络调用放入 Agent 数据库事务。

### 已确认 A 的具体边界

1. 在 Agent 自有草稿表增加正整数 `revision`，旧行和新草稿从 1 开始；草稿读取/编辑响应返回它。它属于这一项草稿内容，不使用整个运行状态作版本。
2. 标题、说明、负责人或解析状态发生实际变化时，短事务保存变化并递增版本；无变化保存不递增。版本比较也适用于文字编辑，不能只保护负责人接口。
3. 文字编辑、本人选择负责人和确认请求必须提交上次读取的版本。先检查当前发起人及团队群资格，再在事务锁定后比较；冲突要求本人重读。版本不是权限凭证，也不是任务创建幂等键。
4. 本人选择仍沿用 A49：可选任一当前可用团队成员，或明确未指派；不能仅把歧义状态的 ID 0 当成已选择未指派。选非零 ID 时由 User 检查当前资格，Task 在创建时继续检查。原提取称呼作为审查依据保留，选择结果必须与自动解析结果区分。
5. 完整唯一匹配可在本人确认整份当前版本草稿时接受；未找到/重名/截断仍须先做选择或明确未指派。不能只凭版本匹配就让未处理歧义通过确认。
6. 首次确认核对版本和全部合法状态后冻结原内容及稳定 Task 请求键。冻结进入 creating 后禁止任何编辑；冻结、创建成功和回帖状态更新不递增内容版本。超时重试继续用同一冻结版本、原内容和请求键，不能因运行状态变化失去重试依据或重建任务。
7. 旧草稿可通过迁移默认版本读取，其原 ID、来源、请求键与任务结果保留。推荐新写入契约统一要求版本：旧客户端缺版本时明确拒绝写入，不默认为 1 或当前版本。Gateway 与页面需一起更新；保留旧请求字段号不等于继续接受缺少版本的旧写入。已有不确定创建结果由新客户端读取固定版本后本人重试。

已确认统一写入契约的理由：项目已约定本地完成后统一部署，可以协调升级；同时保留两个写入模式会增加判断分支，并可能让旧入口绕过新比较。代价是接线阶段旧页面暂不能执行受影响写入，须在最终部署前完成 Gateway/页面更新。若用户选择 B，则保持旧值契约并明确新增字段的存在性，不能把未传负责人 ID 默认为零来跳过审查。

### 确认后的分步范围

以下保留完整负责人链的分步范围；本轮先完成第 1 项和现有文字写入的 Gateway/页面版本接线，负责人选择相关部分仍待实现：

1. 实现 Agent 版本/旧值比较基础、事务冲突与冻结重试规则及定向测试。选择 A 时预计涉及 `rpc/agent/task_draft.go`、`draft_store.go`、`draft_confirm_store.go`、协议/响应/生成代码、新迁移与初始化；不修改 User 数据归属。
2. 实现本人选择接口，复用当前 User 资格检查与 IM 群权限；扩展编辑/确认编排并验证重名处理、明确未指派、权限撤销和重复请求。预计涉及 `rpc/agent/draft_access.go`、`draft_rpc.go`、确认处理器及相应测试；具体接口在决定机制后逐步说明。
3. 单独接 Gateway/原生页面：透出版本和解析状态，提供当前成员选择与重读冲突；每次写入提交本人看到的比较依据，再验证请求取消、旧窗口及任务重试。预计涉及 `api/agent_draft.go`、编辑/确认入口、新选择入口、`examples/chat.html` 与现有页面测试。

拟验收重点：两个窗口不能覆盖彼此；负责人改后又恢复不会让旧版本通过；无变化操作不递增；原未处理歧义不能确认；缺少版本/比较字段的旧请求不能绕过审查；冻结后编辑拒绝；Task 响应丢失后仅重试原任务；已有成功任务/回帖仍保持原结果。真实 MySQL 事务、迁移与浏览器验收仍留到最终环境。

### 决定与实现的关系

讨论阶段仅更新本方案、架构记录和项目进度，等待用户选择；用户明确选择 A 后才实施第 10 节的版本基础。本节描述完整规则，尚未实现的负责人选择不能标成已完成。

## 10. 草稿版本基础与现有写入接线审查（2026-10-03）

用户明确选择 A50 的 A 后实施。本轮只完成版本基础，给后续本人负责人选择提供共同并发依据；尚未新增选择接口、成员下拉框，也未解除含称呼草稿的确认保护。

### 三个独立小步骤

1. **Agent 版本和事务。** 解决旧窗口无法知道整份草稿已变化的问题。草稿读取返回 revision，017 给旧行默认 1；现有文字编辑/确认请求必须传正版本。编辑与冻结在原短事务锁定后再次比较，内容变化才加 1；no-op 不写 UPDATE。原标题/说明比较保留为额外核对，没有缺版本兼容写入分支。版本耗尽拒绝编辑。冻结、保存任务结果与回帖不增内容版本，超时重试仍是原版本/原 Task 请求键。
2. **Gateway 契约。** 解决浏览器大整数精度和旧写入绕过比较的问题。HTTP revision/expected_revision 用规范十进制字符串，Gateway 转 RPC int64，缺失、数字、null、前导零及越界拒绝。GET 兼容无版本旧响应只读；编辑成功必须有正版本，确认结果必须与请求版本一致。仍只转发原 Token，不访问 Agent 数据库。
3. **原生页面。** 解决页面保存或确认使用过时快照的问题。保存/确认提交最近读取或保存成功的版本，成功更新快照；冲突保留本人输入，要求 Load 后再提交。无版本响应可查看，保存/确认禁用，直接调用也拒绝。版本作为请求依据保存，不新增面向用户的技术字段。

调用链：页面 Load → Gateway GetTaskDraft → Agent 验证本人当前资格并读取版本；保存/确认 → Gateway 校验版本字符串 → Agent 再查当前资格 → 锁定草稿比较版本 → 保存文字/冻结意图。只有已冻结意图提交后才调用 Task，任务键不由页面或版本生成。

### 选型和实现取舍

A50 已确认版本号；备选扩大旧值比较、选择理由、迁移/协调升级代价和用户明确回答见第 9 节及[架构记录](architecture-decisions.md)。普通实现保留旧标题/说明字段作为额外检查，暂不移除旧字段号，避免夹带契约整理；这不允许缺版本写入。版本附在内部运行读写结果而未参与 Task 创建内容，避免把内容版本误当幂等键。既有权限、服务边界、同步确认和原 Task 重试规则继续沿用。

### 全部实际修改文件（38 个）

文件数包括既有读取、编辑、确认、启动、回帖测试的版本字段与 SQL 行适配，及 protobuf 生成代码。没有增加 38 个业务功能；这些适配用于维持原链路回归。本清单按本轮开始时的工作区快照比较，不把此前未提交的重构混入。

| 文件定位 | 本轮实际改动 |
| --- | --- |
| [api/README.md](../api/README.md) | HTTP 版本字段、升级与只读兼容说明 |
| [api/agent_confirm_flow_test.go](../api/agent_confirm_flow_test.go) | 确认组合测试的持久行版本 |
| [api/agent_draft.go](../api/agent_draft.go) | 返回版本字符串与规范版本解析 |
| [api/agent_draft_confirm.go](../api/agent_draft_confirm.go) | 确认必传版本并核对结果版本 |
| [api/agent_draft_confirm_test.go](../api/agent_draft_confirm_test.go) | 确认请求/响应替身适配 |
| [api/agent_draft_edit.go](../api/agent_draft_edit.go) | 编辑必传版本并拒绝无版本结果 |
| [api/agent_draft_edit_test.go](../api/agent_draft_edit_test.go) | 编辑契约与 Unicode 上限请求适配 |
| [api/agent_draft_reply_test.go](../api/agent_draft_reply_test.go) | 原回帖测试草稿响应版本适配 |
| [api/agent_draft_revision_test.go](../api/agent_draft_revision_test.go) | 非法版本与大整数精度验证 |
| [api/agent_draft_test.go](../api/agent_draft_test.go) | 读取草稿响应版本适配 |
| [cmd/agent/confirmation_test.go](../cmd/agent/confirmation_test.go) | 生产启动确认请求/行版本适配 |
| [cmd/agent/main_test.go](../cmd/agent/main_test.go) | 启动读取行版本适配 |
| [deploy/README.md](../deploy/README.md) | 017 执行顺序和协调升级说明 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | 新库草稿版本默认 1 |
| [deploy/mysql/migrations/017_agent_draft_revision.sql](../deploy/mysql/migrations/017_agent_draft_revision.sql) | 已有草稿增量版本列 |
| [docs/agent-assignee-design.md](agent-assignee-design.md) | A50 确认与本轮全部审查定位 |
| [docs/architecture-decisions.md](architecture-decisions.md) | 所选/备选、理由、代价及确认来源 |
| [docs/project-plan.md](project-plan.md) | 实际进度与尚未完成的负责人链 |
| [examples/chat.html](../examples/chat.html) | 保存/确认提交最近版本，无版本只读 |
| [examples/chat.test.cjs](../examples/chat.test.cjs) | 原契约适配及三个页面版本用例 |
| [rpc/agent/README.md](../rpc/agent/README.md) | RPC 版本语义与迁移说明 |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 追加 revision/expected_revision 字段 |
| [rpc/agent/draft_access.go](../rpc/agent/draft_access.go) | 编辑读版本比较与返回递增版本 |
| [rpc/agent/draft_access_test.go](../rpc/agent/draft_access_test.go) | 授权读取草稿版本适配 |
| [rpc/agent/draft_assignee_flow_test.go](../rpc/agent/draft_assignee_flow_test.go) | 负责人后端组合测试版本适配 |
| [rpc/agent/draft_confirm_rpc.go](../rpc/agent/draft_confirm_rpc.go) | 确认编排比较版本，沿用固定任务键 |
| [rpc/agent/draft_confirm_rpc_test.go](../rpc/agent/draft_confirm_rpc_test.go) | 确认/重试请求及存储替身适配 |
| [rpc/agent/draft_confirm_store.go](../rpc/agent/draft_confirm_store.go) | 锁定草稿比较版本并保留冻结版本 |
| [rpc/agent/draft_confirm_store_test.go](../rpc/agent/draft_confirm_store_test.go) | 锁定行/冻结请求版本适配 |
| [rpc/agent/draft_edit_rpc_test.go](../rpc/agent/draft_edit_rpc_test.go) | 编辑协议适配并断言成功返回版本 2 |
| [rpc/agent/draft_edit_test.go](../rpc/agent/draft_edit_test.go) | 编辑事务锁定和递增 SQL 断言 |
| [rpc/agent/draft_revision_test.go](../rpc/agent/draft_revision_test.go) | 恢复原值、no-op、缺版本、重放及上限保护 |
| [rpc/agent/draft_rpc.go](../rpc/agent/draft_rpc.go) | 写入版本参数与读取响应映射 |
| [rpc/agent/draft_rpc_test.go](../rpc/agent/draft_rpc_test.go) | 读取协议行版本适配 |
| [rpc/agent/draft_store.go](../rpc/agent/draft_store.go) | 持久读取及锁定编辑版本比较/递增 |
| [rpc/agent/draft_store_test.go](../rpc/agent/draft_store_test.go) | 读取行版本列适配 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 用已有 protoc 工具更新消息生成代码 |
| [rpc/agent/task_draft.go](../rpc/agent/task_draft.go) | 运行持有草稿版本，新草稿从 1 开始 |

### 验证结果与未验证部分

- Agent/进程/Gateway 定向回归通过；全量 `go test ./... -count=1` 通过。补充版本上限/返回值测试及移除无关错误替身字段后，Agent 定向回归通过。
- `node --test examples/chat.test.cjs` 80 项通过；新增大版本字符串连续保存/确认、无版本只读、文字恢复原值仍需重读的三个用例。
- Linux Agent 和 Gateway 编译通过。首次受沙箱 Go 缓存访问限制，获得权限后构建通过。
- SQL 替身验证事务内比较、no-op 无 UPDATE、过时版本回滚、冻结重放版本/任务键不变，初始化/迁移默认值一致；本机 gRPC 回归验证写入缺版本拒绝及编辑返回新版本。既有 Task 响应丢失和回帖重试回归保持通过。
- 没有执行真实 MySQL 017（以及此前未执行的 016）迁移，没有请求方舟模型，真实数据库并发、浏览器、容器与云端链路尚未验收。测试替身不能替代最终环境验收。

下一步：本人负责人审查与选择使用此版本机制，完成选择状态与确认规则后再接 Gateway/页面。阶段 6 仍在实现中，负责人完整链、模糊时间、多项草稿及群内 @AI 尚未完成。

## 11. 负责人选择闭环与并行集成审查（2026-10-03）

本批按用户确认的 worktree 方案执行：主 agent 统一契约、审查、提交和集成，三位执行 agent 分别在独立后端/Gateway/页面 worktree 工作。共同基线 `2daef47`，后端 `4c70758`、Gateway `eebb365`、页面 `576907a`；按该顺序合入 `codex/assignee-integration`，没有冲突。main 仍为 `7a717cc`，合入须经用户审查，本批没有推送或部署。各子 agent 只改任务范围内文件，没有自行创建迁移或更改依赖。

### 九个小步骤、目的与结果

| 步骤 | 目标与解决的问题 | 实际改动和验证 |
| --- | --- | --- |
| 1：主 agent 统一契约 | 三层使用相同的状态、版本和审查字段，避免各自实现不同含义 | 固定 optional ID、selected/unassigned、HTTP 字符串契约及文件归属；协议生成与 Agent/进程/API 基线测试通过。初次输出目录错误已纠正，没有残留错误生成文件 |
| 2：后端选择保存 | 只有原发起人可以保存当前真实成员或明确未指派，不能覆盖并发编辑 | 新选择 RPC、User 资格检查、事务内完整草稿/版本比较；保留原称呼，实际变化加一、相同选择 no-op |
| 3：后端确认审查 | 版本不能代替本人看过负责人，歧义不能默认为零 | optional reviewed ID 区分缺失与零；编排与冻结事务均核对。待确认正 ID 复核当前成员；冻结后保持原 ID/版本/任务键 |
| 4：后端业务回归 | 新规则不能破坏已有创建和回帖恢复 | Agent 与进程全包回归通过，覆盖权限撤销、版本过时、冻结禁改、缺审查、明确零、目标离队及文字保留选择；旧过时确认保护包装删除 |
| 5：Gateway 展示/选择 | 页面需要看到原称呼和状态，并有独立保存入口 | 共享响应始终输出两字段，新增 PUT 路由及严格 ID/版本校验；返回状态/ID/版本异常拒绝，含版本加一溢出保护 |
| 6：Gateway 确认 | HTTP 不能丢失审查值，不能把缺失当零 | optional 字段透传原 Token，成功核对负责人/版本；API 回归通过，新增 9 个处理器测试含实际 JSON 输出及大整数 |
| 7：页面展示与目录 | 本人能识别原称呼和当前实际 ID，重名不靠猜测 | 显式加载已有成员分页目录；显示匹配/歧义状态，下拉框分开未选择与明确未指派，不假定第一页完整 |
| 8：页面保存与确认 | 未保存选择不能用于创建，旧请求结果不能误开放新上下文 | 保存携带最新版本，确认携带已保存 ID；所有草稿操作互斥，冲突保留输入并要求重读，身份/范围切换丢弃旧结果；新增 20 项页面测试 |
| 9：主 agent 集成 | 替身单测通过还需要检查三层组合与其他业务回归 | 实际 HTTP→TCP gRPC Agent→SQL/业务替身测试通过；全量 Go、页面 100 项及 Linux Agent/Gateway 编译通过，更新计划、ADR、接口和验收记录 |

调用链：页面显式加载 User 团队成员目录 → 选择 PUT 经 Gateway → Agent 以原 Token 核对本人/当前群与目标成员 → 短事务保存选择并返回版本 → 页面审查后确认 → Agent 比较版本/文字/负责人并冻结 → Task 使用固定键创建 → Agent 保存原任务 ID → 按既有配置尝试独立机器人回帖。成员资料仍归 User，草稿选择仍归 Agent，任务创建规则仍归 Task；没有跨服务表查询或后台自动重试。

### 全部实际修改文件：37 个

以下清单以 `main 7a717cc` 为基准，包含共同协议、生成代码、测试和文档，不把不同 worktree 的相同文件重复计算。仅新增本批相关内容，没有更换语言/框架、部署配置或新增数据库迁移。

| 文件 | 用途 |
| --- | --- |
| [.gitignore](../.gitignore) | 忽略独立 worktree 目录 |
| [AGENTS.md](../AGENTS.md) | 保存用户确认的协作、文件归属和批次规则 |
| [api/README.md](../api/README.md) | HTTP 选择、元数据及确认审查契约 |
| [api/agent_draft.go](../api/agent_draft.go) | 共享字段与负责人形状校验 |
| [api/agent_draft_assignee.go](../api/agent_draft_assignee.go) | 专用选择 HTTP 处理器 |
| [api/agent_draft_assignee_test.go](../api/agent_draft_assignee_test.go) | 选择、元数据、版本及错误用例 |
| [api/agent_draft_confirm.go](../api/agent_draft_confirm.go) | 确认 optional reviewed ID 解析/转发 |
| [api/agent_draft_confirm_assignee_test.go](../api/agent_draft_confirm_assignee_test.go) | 缺失、零、大 ID 和异常确认结果 |
| [api/assignee_flow_integration_test.go](../api/assignee_flow_integration_test.go) | 实际 HTTP/TCP gRPC 选择、审查及冻结重试组合 |
| [api/main.go](../api/main.go) | 新 PUT 路由，15 秒预算 |
| [cmd/agent/main_test.go](../cmd/agent/main_test.go) | 现有启动 User 替身补目标成员检查 |
| [docs/agent-assignee-design.md](agent-assignee-design.md) | 本文及完整审查清单 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A49/A50 内方案、备选、代价和验证记录 |
| [docs/assignee-collaboration-contract.md](assignee-collaboration-contract.md) | 三个 worktree 的共同协议和归属 |
| [docs/project-plan.md](project-plan.md) | 当前本地能力、main 审查状态和下一步 |
| [docs/stage6-acceptance.md](stage6-acceptance.md) | 更新负责人/版本及真实验收场景 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | 用户采用的协作方案与执行记录 |
| [examples/chat.html](../examples/chat.html) | 原生页面目录、选择、审查与上下文保护 |
| [examples/chat.test.cjs](../examples/chat.test.cjs) | 新增 20 项，页面总计 100 项 |
| [rpc/agent/README.md](../rpc/agent/README.md) | 选择、确认保护、冻结重试说明 |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 选择 RPC 和 optional reviewed ID，保留已有字段号 |
| [rpc/agent/draft_access.go](../rpc/agent/draft_access.go) | reader 接入选择存储和成员客户端 |
| [rpc/agent/draft_access_test.go](../rpc/agent/draft_access_test.go) | 原访问替身声明适配 |
| [rpc/agent/draft_assignee_confirm_test.go](../rpc/agent/draft_assignee_confirm_test.go) | 审查、冻结、目标资格与文字保留选择用例 |
| [rpc/agent/draft_assignee_test.go](../rpc/agent/draft_assignee_test.go) | 原确认保护测试适配审查方法 |
| [rpc/agent/draft_confirm_rpc.go](../rpc/agent/draft_confirm_rpc.go) | 待确认成员检查、审查 ID 和完整冻结草稿核对 |
| [rpc/agent/draft_confirm_rpc_test.go](../rpc/agent/draft_confirm_rpc_test.go) | 原 confirmer 替身签名适配 |
| [rpc/agent/draft_confirm_store.go](../rpc/agent/draft_confirm_store.go) | 锁定后比较负责人审查值 |
| [rpc/agent/draft_confirm_store_test.go](../rpc/agent/draft_confirm_store_test.go) | 原冻结事务测试签名适配 |
| [rpc/agent/draft_revision_test.go](../rpc/agent/draft_revision_test.go) | 原版本回归签名适配 |
| [rpc/agent/draft_rpc.go](../rpc/agent/draft_rpc.go) | 生产 ConfigureDraftAccess 接入现有 User 客户端/存储 |
| [rpc/agent/draft_select_rpc.go](../rpc/agent/draft_select_rpc.go) | 本人选择 RPC 和目标成员核对 |
| [rpc/agent/draft_select_store.go](../rpc/agent/draft_select_store.go) | 事务版本比较、no-op 与选择写入 |
| [rpc/agent/draft_select_test.go](../rpc/agent/draft_select_test.go) | 选择、明确零、权限、状态和事务用例 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 重新生成消息定义 |
| [rpc/agent/pb/agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) | 重新生成选择 RPC 客户端/服务声明 |
| [rpc/agent/task_draft.go](../rpc/agent/task_draft.go) | selected/unassigned 合法组合及审查规则 |

### 验证结果与未验证部分

- 共同起点及执行分支定向回归通过；集成后全量 `go test ./... -count=1` 通过。
- `TestAssigneeHTTPAndAgentRPCReviewSelectionAndFrozenRetry` 单独及全量回归通过：大整数 ID `9007199254740993` 不失真；歧义/旧确认拦截；保存选择后版本更新；Task 首次超时后重读 creating，目标资格撤销仍使用冻结 ID/版本/键恢复原任务；重复成功确认不再创建，发起人群资格撤销仍拒绝。
- `node --test examples/chat.test.cjs` 100/100 通过，覆盖选择/分页/显式零、未保存保护、冲突保留输入、上下文切换、互斥和冻结结果。
- Linux `CGO_ENABLED=0` 编译 `./cmd/agent` 和 `./api` 通过，产物放在临时目录；未构建容器。
- SQL、User/IM/Task 业务和模型使用替身；组合测试运行生产 Agent 处理与存储适配、本机 HTTP/TCP gRPC，不运行实际 Gateway/Agent 进程或真实 MySQL。没有执行 016/017 或此前真实迁移，没有请求方舟，没有真实浏览器/中间件/容器/云端验收。

当前阶段 6 仍未全完成。用户审查本集成结果后再合入 main；下一业务批次先讨论时间处理，再逐步推进多项草稿和群内 `@AI`，阶段 7 仍须真实部署与整体验收。既定选型内接口取舍记录在 A49/A50 补充，新的技术或架构选择仍先讨论。

