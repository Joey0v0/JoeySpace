# 单项自动截止时间：本批审查

2026-10-03。本批唯一目标：把已验证的固定参考请求和 Go 有限解释器接入单项任务草稿，让本人能审查时间依据、处理模糊时间后再创建任务。沿用用户已明确选择的 A51/A52/A53，不新增框架、中间件或服务，不改变数据归属、权限、同步确认及本人显式重试规则。

用户要求继续后，上一批 `89fdceb` 已快进合入 main。本批共同起点 `3c7aaf6`，交付位于 `codex/deadline-auto-integration`，main 仍为 `89fdceb`，新结果待用户审查；没有推送或部署。

## 1. 九个小步骤与实际成果

| 步骤 | 要做什么、解决的问题 | 实际改动范围与结果 |
| --- | --- | --- |
| 1：主 agent 共同契约 | 统一三层时间依据及兼容规则，避免各 agent 对模糊时间的理解不同 | proto、正确目录 pb、初始化、018 迁移与共同契约；新增九字段依据和确认状态审查；准备阶段不标业务完成 |
| 2：后端生成 | 模型只能提取原文，不能决定可信 UTC 或参考时间 | Eino 严格七字段输出；prepare 核对指定来源并调用既有 Go 解释器，当前候选与完整依据进入草稿 |
| 3：后端持久/读回 | 刷新后仍能审查当时的解释，不能只剩 due=0 | Agent 同事务保存九字段；普通及加锁查询、RPC 读回均保留；旧空默认不编造新证据，损坏数据拒绝 |
| 4：后端本人处理/确认 | 区分“尚未明确时间”与“本人明确不设”，防止零值绕过确认 | 正时间 selected、零 unset；状态变化递增版本；新依据确认要求时间与状态，needs_input 拒绝；冻结/重试不重算 |
| 5：Gateway 读回 | 页面拿到完整、精确、合法的审查依据 | 九字段 JSON 与来源消息字符串 ID；GET/编辑/确认/回帖共享形状验证，异常响应按既有 502 处理 |
| 6：Gateway 写入 | HTTP 确认必须传递本人审查的状态，不能替后端判断时间 | 严格解析 expected_deadline_resolution；确认结果状态一致；时间编辑结果须 selected/unset，业务冲突保留 409 |
| 7：页面依据展示 | 本人能看到“哪个原文、按什么时刻、得到什么候选” | 安全文本展示原文、来源、上海时区、有效参考、原候选/原因及首次指令参考；持久依据与当前生成参考分开 |
| 8：页面处理与确认 | 模糊表达需要实际保存决定，输入空不代表已处理 | needs_input 阻止确认；补时间或明确保存零后才可确认；状态变化检查新版本；其他写入/确认/回帖不能悄改原证据 |
| 9：主 agent 组合验证 | 验证三层共同工作，并保留既有失败/重试能力 | 新实际 HTTP/TCP gRPC 自动时间组合，适配原参考组合；全量 Go、140 项 Node、Linux 构建通过，计划/选型/验收与完整文件记录更新 |

三个子 agent 分别承担步骤 2—4、5—6、7—8，主 agent 管共同文件和集中集成；上限按全批计算，未扩成每人九步。完整[共同契约](deadline-auto-contract.md)、[选型与备选理由](architecture-decisions.md#a51a52a53-自动时间闭环实施取舍2026-10-03)、[并行记录](worktree-collaboration-plan.md#12-自动时间闭环批次2026-10-03)。

## 2. 调用链与本人看到的行为

```text
页面首次提交固定参考与生成键 → Gateway → Agent 验证本人/当前群资格
  → IM 授权消息 → Eino 只提取时间原文与来源
  → Go 核对原文并有限解释 → Agent MySQL 适配器保存草稿及原依据
本人读取/补充/明确不设 → Gateway → Agent 加锁、核对版本并保存状态
本人确认时间+状态+版本 → Agent 冻结 → Task 按稳定键创建最终截止时间
```

- “明天 15:30”按指定来源参考及 Asia/Shanghai 得到候选，显示为 `parsed`；仍须本人审查后确认。模型未提供可信时间戳。
- “明天下午”不补具体小时，保存为 `needs_input`，不能调用 Task。本人填完整时间并保存变 `selected`，或明确保存零变 `unset`；即使 due 都是零，处理状态变动也使版本加一。
- 时间来源可以是本人指令，或一个实际授权的群文本消息；时间来源消息不必等于任务来源消息。消息里的“明天”按原消息时间，本人指令按首次提交参考。两种参考分别保存，重放与冻结不再解释。
- 人工修改只改变当前最终时间和处理状态；原文、来源、原参考、原候选和原因不变，便于本人发现解释偏差。Task 只使用最终已保存的 due，不使用已被本人覆盖的原候选。
- 缺少首次指令参考的旧生成请求不补当前时刻；相对表达需本人处理。非法消息时间、假来源或模型注入可信元数据直接拒绝，不能当普通无截止时间保存。

## 3. 兼容与部署影响

Agent 拥有时间依据，九列加入已有草稿表，初始化和增量脚本一致。已有库需要先完成 017，再执行[018](../deploy/mysql/migrations/018_agent_draft_deadline.sql)，然后升级 Agent；即使只读取旧草稿，也依赖新增列。**本批没有执行真实迁移。**

旧行九字段全空/零代表 legacy，输出省略 deadline，保留既有时间审查规则；不会重算旧运行或修改请求指纹。新输出有完整九字段对象，即使无时间也保留 none 及首次指令参考；新确认须显式提交当前状态和时间（包括零），旧请求不能绕过新状态。模糊 needs_input 始终不可确认。

Agent、Gateway、页面需协调升级；模型输出变为必填七字符串字段，新增三个原文证据字段。没有新增端口、服务、依赖或 Docker 目标。现有锁、内容版本、权限复核、幂等键和本人显式重试沿用。客户端参考仅用作本人审查的解释依据，不作为权限或可信服务审计时刻。

## 4. 实际验证与边界

- `go test ./... -count=1` 全量通过。
- `node --test examples/chat.test.cjs` **140/140** 通过，原 126 项保留，新增 14 项覆盖依据形状/展示、模糊清空、人工覆盖、版本语义、证据漂移及上下文隔离。
- Linux amd64 Agent/Gateway 编译通过，`git diff --check` 通过。
- [自动时间组合](../api/deadline_auto_flow_test.go)实际走本机 HTTP/TCP gRPC，使用生产 Agent prepare/store/confirm、Eino 和 GORM 适配器；SQL、User/IM/Task 与模型为替身。两种场景验证完整 INSERT/SELECT 依据、超大来源 ID 精度、消息/指令参考独立、原候选人工覆盖、模糊明确零、缺/错审查、旧版本、冻结禁止编辑、Task 超时后固定键/最终值重试、重复成功不再次调用 Task，以及当前群资格撤销。
- [原参考组合](../api/deadline_reference_flow_test.go)同步七字段假模型和九列 INSERT 预期，保留模型失败后原参考重试、同键重放、指纹及冲突/撤权断言。必要旧测试仅适配签名、SQL 字段与合法 none 输入，不移除原业务断言。
- 后端专门验证模型可信元数据拒绝、原来源核对、持久字段/加锁读回、旧全空兼容、损坏存储拒绝、状态变动/no-op 与确认不调用 Task 的拒绝路径；Gateway 验证状态输入及完整响应，页面所有响应仍有上下文/互斥保护。

本机 Go 缓存/工具权限按已有工具审批执行，没有安装新工具或改全局 Go 配置。首次新增组合测试编译时补齐 Task 替身的 gRPC Unimplemented 嵌入，修正后定向和全量通过；没有放宽生产校验。

**未验证：**真实 MySQL 迁移、锁/并发及进程崩溃恢复，真实方舟模型的原文提取效果，真实浏览器、容器与云端联调。真实模型接入点与预算未定，不发送模型请求。当前是有限格式解释，不承诺识别所有自然语言；多项草稿及群内 `@AI` 尚未实现，阶段 6 未全部完成。下一批多项确认单位、状态及部分成功需先讨论。

## 5. 全部实际修改文件

相对 main `89fdceb` 共 **45 个文件**，无文件删除。文件数包括生成代码、既有测试适配及审查文档；业务目标仅为单项自动时间闭环。`agent_grpc.pb.go` 生成结果未变化，不列入修改。

| 文件 | 本批用途 |
| --- | --- |
| [api/README.md](../api/README.md) | 时间依据 HTTP、状态审查和兼容说明 |
| [api/agent_draft.go](../api/agent_draft.go) | 共享输出挂载合法完整依据 |
| [api/agent_draft_confirm.go](../api/agent_draft_confirm.go) | 确认状态输入/转发及结果一致 |
| [api/agent_draft_confirm_metadata_test.go](../api/agent_draft_confirm_metadata_test.go) | 确认状态严格解析与响应检查 |
| [api/agent_draft_deadline.go](../api/agent_draft_deadline.go) | 时间保存状态 selected/unset 检查 |
| [api/agent_draft_deadline_metadata.go](../api/agent_draft_deadline_metadata.go) | 九字段输出及形状验证 |
| [api/agent_draft_deadline_metadata_test.go](../api/agent_draft_deadline_metadata_test.go) | 来源、状态、原因与兼容组合 |
| [api/deadline_auto_flow_test.go](../api/deadline_auto_flow_test.go) | 实际 HTTP/TCP gRPC 自动时间全链组合 |
| [api/deadline_reference_flow_test.go](../api/deadline_reference_flow_test.go) | 原参考链适配七字段/九列并保留断言 |
| [deploy/README.md](../deploy/README.md) | 018 顺序与协调升级说明 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | 新库草稿九字段 |
| [deploy/mysql/migrations/018_agent_draft_deadline.sql](../deploy/mysql/migrations/018_agent_draft_deadline.sql) | 既有库增量，空默认保留旧数据 |
| [docs/architecture-decisions.md](architecture-decisions.md) | 既定 A51—A53 本批选择/备选/代价及验证 |
| [docs/deadline-auto-contract.md](deadline-auto-contract.md) | 共同字段、状态、兼容和角色范围 |
| [docs/deadline-auto-review.md](deadline-auto-review.md) | 九步、完整文件和验证边界 |
| [docs/deadline-reference-review.md](deadline-reference-review.md) | 前批 main 合入和历史范围说明 |
| [docs/project-plan.md](project-plan.md) | 实际进度与下一步多项讨论 |
| [docs/stage6-acceptance.md](stage6-acceptance.md) | 本地自动时间记录和真实待验收场景 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | 三角色时间、提交及合并记录 |
| [examples/chat.html](../examples/chat.html) | 依据展示、模糊处理和确认状态 |
| [examples/chat.test.cjs](../examples/chat.test.cjs) | 新 14 项及原 126 项回归 |
| [rpc/agent/README.md](../rpc/agent/README.md) | 模型/依据/状态/迁移与确认说明 |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 九字段嵌套依据及确认状态字段 |
| [rpc/agent/draft_access.go](../rpc/agent/draft_access.go) | 存储依据形状检查 |
| [rpc/agent/draft_assignee_confirm_test.go](../rpc/agent/draft_assignee_confirm_test.go) | 原负责人确认签名适配 |
| [rpc/agent/draft_assignee_flow_test.go](../rpc/agent/draft_assignee_flow_test.go) | 原负责人模型/INSERT 合法 none 适配 |
| [rpc/agent/draft_confirm_rpc.go](../rpc/agent/draft_confirm_rpc.go) | RPC 状态校验及确认传递 |
| [rpc/agent/draft_confirm_rpc_test.go](../rpc/agent/draft_confirm_rpc_test.go) | 原确认签名兼容回归 |
| [rpc/agent/draft_confirm_store.go](../rpc/agent/draft_confirm_store.go) | 锁定读回完整依据和冻结审查 |
| [rpc/agent/draft_confirm_store_test.go](../rpc/agent/draft_confirm_store_test.go) | 原冻结/事务测试签名适配 |
| [rpc/agent/draft_deadline_auto_test.go](../rpc/agent/draft_deadline_auto_test.go) | 自动提取/持久/语义版本/拒绝及重试 |
| [rpc/agent/draft_deadline_metadata.go](../rpc/agent/draft_deadline_metadata.go) | 可比较九字段与状态审查/兼容规则 |
| [rpc/agent/draft_deadline_store.go](../rpc/agent/draft_deadline_store.go) | 正时间 selected、零 unset 与状态版本 |
| [rpc/agent/draft_deadline_test.go](../rpc/agent/draft_deadline_test.go) | 原手工时间确认签名适配 |
| [rpc/agent/draft_preparer.go](../rpc/agent/draft_preparer.go) | 来源核对与有限解释接生成 |
| [rpc/agent/draft_preparer_test.go](../rpc/agent/draft_preparer_test.go) | 原生成合法 none/SQL 预期适配 |
| [rpc/agent/draft_reference_test.go](../rpc/agent/draft_reference_test.go) | 新 none 指令参考写入与旧摘要回归 |
| [rpc/agent/draft_revision_test.go](../rpc/agent/draft_revision_test.go) | 原版本冻结签名适配 |
| [rpc/agent/draft_rpc.go](../rpc/agent/draft_rpc.go) | RPC 草稿带完整依据 |
| [rpc/agent/draft_store.go](../rpc/agent/draft_store.go) | 九列写入/读回与旧全空转换 |
| [rpc/agent/draft_store_test.go](../rpc/agent/draft_store_test.go) | 原草稿 INSERT 九列预期适配 |
| [rpc/agent/eino_task_draft.go](../rpc/agent/eino_task_draft.go) | 模型七字符串、原文证据提取/严格校验 |
| [rpc/agent/eino_task_draft_test.go](../rpc/agent/eino_task_draft_test.go) | 模型七字段及原测试回归 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 正确目录 protobuf 生成结构 |
| [rpc/agent/task_draft.go](../rpc/agent/task_draft.go) | 草稿保存可比较依据、字段验证 |

## 6. 三 agent 集成记录

后端 `175820c`（21 文件）、Gateway `f09eb3d`（6 文件）、页面 `3978fab`（2 文件）。主 agent 依次后端快进、Gateway 合并 `0711f49`、页面合并 `555f9be`，无冲突；共同文件和最后两项组合测试/文档由主 agent 完成。三个独立 worktree 保留，执行 agent 未自行提交/合并/推送。

执行窗口 UTC 14:02:39—14:25:47，**23 分 08 秒**；各角色耗时后端 23:08、Gateway 11:27、页面 13:09，详细见协作记录。不含共同准备、主 agent 审查/验证/文档，也无同任务单 agent 对照，不据此声称倍数提速。
