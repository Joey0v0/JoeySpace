# 单项任务草稿确认方案与本轮审查记录

日期：2026-10-02。关联选型：A41。用户明确选择 A：同步确认＋持久记录，本人显式重试。后端存储、确认 RPC、生产构造、Gateway 和原生页面确认入口已实现并做本地测试。迁移尚未执行，真实持久化、故障恢复、模型及浏览器未验收。

## 1. 目标与已有依据

把阶段 6 已有的“生成、读取、编辑草稿”接到真正的任务创建。沿用 A35—A38：草稿和运行记录归 Agent；只有发起人本人可确认；Agent 核对当前团队群资格；实际创建由 Task RPC 再核对权限。

已审查 [任务创建](../rpc/task/create.go)：同一创建者、同一请求键、同一内容返回原任务 ID；唯一约束处理并发重复写入。同键不同内容返回冲突。这是可复用的代码能力，真实 MySQL 并发行为仍待验收。

需要解决的间隙：Task 已创建任务，Agent 却因网络超时或进程退出没有记下结果。不能将超时解释为“任务没创建”，也不能改内容、换请求键再重试。

## 2. 候选方案

| 方案 | 执行方式 | 优点 | 代价 |
| --- | --- | --- | --- |
| A（推荐） | 同步确认 RPC，先持久锁定草稿，再调用 Task；不确定结果由发起人显式重试 | 复用现有调用链和原用户 Token，不增加中间件；重启后能根据记录继续相同操作 | 请求仍可能超时；没有后台自动恢复；发起人须保有当前权限才能重试 |
| B | 确认只保存待执行记录，后台工作进程创建任务，页面查询结果 | 不依赖长 HTTP 请求，可自动重试和恢复 | 需增加持久工作调度、并发领取与恢复；须另定后台执行身份及权限，不能简单保存用户 JWT 供长期重放 |

两种方案都保持 Agent/Task 的既有数据边界。B 也可以先使用 MySQL 持久工作记录，不必立即新增消息队列。第一版只处理一项任务；多项、群回帖和自动恢复的长期路线仍保留。

## 3. 推荐 A 的确认链路

1. 页面只确认当前已保存并展示的草稿；有未保存改动时先保存并审查。确认请求带运行 ID 与上次读取的标题/说明，不允许浏览器指定创建请求键。
2. Gateway 转发原 Token。Agent 经 User 确定用户，按发起人读取记录，并经 IM 核对当前团队群范围；失败时不发创建请求。
3. Agent 在短事务中锁定运行与第 0 项草稿，复核状态和客户端看到的文字。旧内容不一致则要求重读。首次确认保存稳定操作键并将状态改为 `creating`，冻结全部创建字段。与编辑复用同一锁定规则，避免编辑和确认同时成功。
4. 提交事务后，Agent 用冻结的字段、原用户 Token 和稳定键调用 Task `CreateTask`。网络请求期间不占用数据库事务锁。
5. Task 返回任务 ID 后，Agent 在短事务中保存第 0 项的任务 ID，并将单项运行置为 `succeeded`；只有保存成功后才向客户端报告成功。
6. 重试必须复用冻结内容和原操作键；若已成功则在重新核对读取权限后返回原任务 ID。若仍是 `creating`，可再次调用 Task 获取或创建同一任务。

建议操作键由 Agent 根据运行 ID 和项序号确定，例如 `agent-task-{run_id}-0`，符合 Task 现有 64 字符限制。它与“生成草稿”的请求键用途不同；不能使用编辑内容生成新键。

并发确认可能产生多次相同 Task RPC，但任务服务的唯一约束只允许保存一个任务。Agent 的成功结果须单调：不得被较晚返回的失败覆盖；重复保存相同任务 ID可成功，不同 ID需报告异常，不覆盖原结果。此方案不宣称网络请求仅执行一次。

## 4. 状态与失败规则

| 持久状态 | 含义 | 允许的操作 |
| --- | --- | --- |
| `waiting_confirmation` | 尚未提交创建，草稿可编辑 | 读取、编辑、首次确认 |
| `creating` | 已锁定创建意图，可能正在调用，也可能已超时等待重试 | 读取、发起人显式重试；禁止编辑和换键 |
| `succeeded` | 已取得并持久保存任务 ID | 读取、重复确认返回原结果；禁止编辑 |

`creating` 不能在页面被描述成“一定仍有后台运行”。本方案没有后台工作进程，页面应提示创建已提交，结果可能待确认，必要时重新读取或显式重试。

- 锁定事务前权限拒绝、文字冲突或数据库故障：不调用 Task，未锁定时仍可编辑；若事务结果不明，先重读状态。
- 锁定后、调用前进程退出：留下相同创建意图，下次重试使用同一内容和键。
- Task 创建成功但响应丢失，或 Agent 保存任务 ID 失败：保留冻结记录，重试 Task 可复用已创建任务。
- Task RPC 返回错误：不自动退回可编辑状态。较早一次调用可能已成功；后续权限拒绝也不能证明任务不存在。界面展示本次错误和待核实状态，不宣称“没有创建”。
- 发起人离队、退出群或账号不可用：遵守 A36，不允许他人接手或绕过当前权限恢复。可能出现 Task 已有任务、Agent 尚无结果的记录；需要后续单独设计核对/管理流程，不能换键补建。

以上选择以不重复创建为优先，代价是已锁定但遇到不可恢复错误的草稿不能直接改后重提。后台恢复、取消及人工核对不是这一步的已实现能力。

## 5. 确定方案后的分步范围

用户选定 A 后，本轮完成存储、确认 RPC 和生产构造接线。下面按业务目标说明范围：

1. 存储状态与结果（本轮已实现）：草稿项新增稳定操作键和可空任务 ID；准备新库定义与增量迁移；测试锁定、编辑冲突、重试及成功结果不回退。涉及 Agent 契约/存储/测试和 MySQL 初始化/迁移。
2. 确认 RPC（本轮已实现）：组合当前身份与范围校验、冻结存储和 Task 客户端，接入生产 Agent 构造；测试重复确认、未知结果、恢复与权限拒绝。涉及 Agent protobuf/生成代码、确认实现、进程接线和对应测试。
3. Gateway 与原生页面（第二轮已实现）：新增明确确认入口，展示任务 ID 和未确定状态；禁止确认未保存内容，显式重试保留同一运行。涉及 API 路由/处理器、页面及测试。第一轮第 3 个实施小步是接入 `cmd/agent`；第二轮第 3 步验证 HTTP→Agent gRPC→Task 替身的完整确认与重试链路。

具体文件清单和接口超时在每个小步开始前说明。若选择 B，先讨论后台身份与授权、工作领取和重试规则，再修改实现计划。

## 6. 后端确认实现的修改文件与验证

以下是本轮实际修改的完整清单；保留其他此前未提交修改。生成文件由项目已有 protobuf 工具生成，未新增依赖。

| 目标 | 文件定位 |
| --- | --- |
| 状态与结果契约 | [task_draft.go](../rpc/agent/task_draft.go) |
| 读取创建键及任务 ID | [draft_store.go](../rpc/agent/draft_store.go) |
| 事务冻结、重试、保存结果及测试 | [draft_confirm_store.go](../rpc/agent/draft_confirm_store.go)、[draft_confirm_store_test.go](../rpc/agent/draft_confirm_store_test.go) |
| 新库字段及已有库迁移 | [init.sql](../deploy/mysql/init.sql)、[012_agent_draft_task_result.sql](../deploy/mysql/migrations/012_agent_draft_task_result.sql) |
| RPC 契约与生成文件 | [agent.proto](../rpc/agent/agent.proto)、[agent.pb.go](../rpc/agent/pb/agent.pb.go)、[agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) |
| Server 确认依赖及结果编码 | [server.go](../rpc/agent/server.go)、[draft_rpc.go](../rpc/agent/draft_rpc.go) |
| 确认权限链、Task 调用及测试 | [draft_confirm_rpc.go](../rpc/agent/draft_confirm_rpc.go)、[draft_confirm_rpc_test.go](../rpc/agent/draft_confirm_rpc_test.go) |
| 共享 gRPC 测试构造 | [draft_rpc_test.go](../rpc/agent/draft_rpc_test.go) |
| 生产启动接线及 TCP 测试 | [main.go](../cmd/agent/main.go)、[confirmation_test.go](../cmd/agent/confirmation_test.go) |
| RPC 与迁移说明 | [Agent README](../rpc/agent/README.md)、[部署 README](../deploy/README.md) |
| 方案、决策和进度 | [本文](agent-confirmation-design.md)、[architecture-decisions.md](architecture-decisions.md)、[project-plan.md](project-plan.md) |

定向测试 `go test ./cmd/agent ./rpc/agent ./api ./rpc/task` 和全量回归 `go test ./...` 均通过。覆盖文字冲突、冻结/结果事务回滚、不同任务 ID 不覆盖原成功、当前资格拒绝、Task 响应丢失与 Agent 结果写入失败后的同键重试、重复确认和大整数任务 ID。生产构造测试用真实本地 TCP gRPC 连接 User/IM/Task 替身、SQL 替身提供记录，模拟保留记录后重新构造 Agent；没有请求真实模型。

未验证：真实 MySQL 的迁移、锁与唯一约束并发行为，真实 Task/业务 RPC 联调、进程终止后的持久恢复、浏览器、容器及云端部署。确认 RPC 总超时 12 秒，Task 调用最多 5 秒，受父请求剩余时间限制；这是既定同步方式下的初始实现值，真实慢请求待验收。

## 7. HTTP与页面确认实现的修改文件与验证

2026-10-02 第二轮沿用 A41，不新增服务、依赖、中间件或权限规则。实现选择为显式按钮确认、失败后手动重读，再根据已保存状态手动重试；不采用自动重发或错误后直接恢复编辑，因为结果可能已写入 Task。代价是用户需多一步核对状态。Gateway 确认路由预算 15 秒，为 Agent 12 秒处理留出传输及错误转换时间，其他路由不变。

| 本轮目标 | 全部实际修改文件 |
| --- | --- |
| HTTP 确认请求和错误映射 | [agent_draft_confirm.go](../api/agent_draft_confirm.go)、[agent_draft_confirm_test.go](../api/agent_draft_confirm_test.go) |
| 任务 ID 返回、结果校验与路由 | [agent_draft.go](../api/agent_draft.go)、[main.go](../api/main.go) |
| 页面确认/重试、冻结结果展示及失败场景 | [chat.html](../examples/chat.html)、[chat.test.cjs](../examples/chat.test.cjs) |
| HTTP→Agent→Task 的本机传输与恢复场景 | [agent_confirm_flow_test.go](../api/agent_confirm_flow_test.go) |
| HTTP、RPC、部署能力说明 | [API README](../api/README.md)、[Agent README](../rpc/agent/README.md)、[部署 README](../deploy/README.md) |
| 方案、选型状态与进度 | [本文](agent-confirmation-design.md)、[architecture-decisions.md](architecture-decisions.md)、[project-plan.md](project-plan.md) |

验证：`node --test examples/chat.test.cjs` 58/58 通过；`go test ./api ./rpc/agent` 和全量 `go test ./...` 通过。页面测试覆盖未保存内容、互斥操作、重复点击、未知结果先读后重试、已成功只显示结果、冻结后覆盖旧编辑文字、上下文变化及非法任务 ID。HTTP 测试覆盖原 Token/旧文字转发，不转发客户端创建键，冲突/权限/超时、结果一致性、Unicode 转义和大整数编码。

本机联调使用真实 HTTP 请求、实际 Gateway 处理器和 Agent 读取/确认实现，通过 TCP gRPC 调用 User/IM/Task 替身；SQL 替身模拟持久记录。首次确认得到超时，重读仍是 `creating`，显式重试得到相同任务 ID，成功的重复确认不再访问 Task；当前群资格撤销后重复确认拒绝。测试未启动生产 Gateway/Agent 进程，也没有真实 Task/MySQL/模型、实际浏览器或容器；不代表上线验收。
