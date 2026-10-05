# 本人从原消息查看后台 @AI 草稿：本批审查

日期：2026-10-05。用户选择 A64 的“从本人原指令消息进入”，本批在 `codex/trigger-status-integration` 交付；三个执行分支已合入本地集成分支，main 仍是上批 `9c45e13`，尚未推送或部署。[契约](trigger-status-contract.md)、[选择和备选](architecture-decisions.md#a64本人发现后台草稿的入口用户选择-a2026-10-05)。

业务目的：前一批 worker 已能在后台保存待审草稿，但关掉浏览器后，发起人没有办法从原指令找到生成结果。本批在已保存的本人 `@AI 整理任务` 消息旁提供只读查看：queued/running 告知稍后重查，exhausted 提示人工排查，completed 才读取原有多项草稿面板逐项审查。不因查看而调用模型、创建 Task、跳过草稿或向群回帖。默认关闭的 worker 不因此被自动开启。

## 九个小步骤与调用链

| 步骤 | 做了什么、为什么、解决的问题 | 实际文件 |
| --- | --- | --- |
| 1 | 记录 A64 与消息 ID 查询、四状态/运行 ID、权限和三工作区边界；追加 RPC 并统一生成代码，让三方在同一契约上实现。 | `docs/architecture-decisions.md`、`project-plan.md`、`trigger-status-contract.md`、`rpc/agent/agent.proto`、`pb/agent.pb.go`、`pb/agent_grpc.pb.go` |
| 2 | Agent inbox 以消息主键只读状态与结果关联，严格拒绝不存在、多行、非法状态/结果组合，不写租约或模型预算。 | `rpc/agent/trigger_status_store.go`、`trigger_status_store_test.go` |
| 3 | Agent 验证本人 Token、原消息发起人和当前团队群资格，之后才查状态；completed 再经现有集合读取复核发起人、团队、群与 run。 | `rpc/agent/trigger_status.go`、`trigger_status_test.go`、`server.go` |
| 4 | Agent 进程复用 worker 已配置的专用 IM mTLS 来源客户端和同一 DB；不开 worker 时安全返回不可用，worker 故障不关闭普通 RPC。 | `cmd/agent/main.go` |
| 5 | Gateway 新增按团队/群/消息 ID 的只读 GET，只转发 Token 和消息 ID，不直读 Agent/IM 表。 | `api/main.go`、`agent_trigger_status.go` |
| 6 | Gateway 校验规范十进制大 ID、回包范围和四状态/运行 ID 组合，安全映射错误并把全部 ID 编成 JSON 字符串。 | `api/agent_trigger_status_test.go` |
| 7 | 页面登录或粘贴 Token 后复用本人资料查询，仅在当前群、本人、已保存的有效指令消息上显示入口；页面过滤仅影响显示，授权仍由 Agent 完成。 | `examples/chat.html`、`chat.test.cjs` |
| 8 | 页面只读查状态，completed 经受控接口调用现有 `MultiDraftController.load(runID)`；切换身份/群或出现并发审查时丢弃旧结果，不覆盖另一集合的未保存编辑。 | `examples/multi-draft-view.js`、`multi-draft-view.test.cjs`，及上一步页面文件 |
| 9 | 主 agent 检查三分支、合入集成分支，集中测试并记录实际能力与未验证项。 | `docs/worktree-collaboration-plan.md`、本文件，及上述实际文件 |

关键只读链：群历史/实时已保存原消息 ID → Gateway GET（本人 Bearer）→ Agent `GetTaskTriggerStatus` → User RPC 验证当前登录人 → 专用 Agent→IM mTLS 按原消息 ID 读取持久来源和当前团队群资格 → 比对原发起人 → Agent inbox 只读状态；completed 再经原集合读取核对 run 归属和当前资格 → 页面原有集合面板读取并等待本人逐项操作。Gateway 验证返回范围，浏览器不传 actor、run ID 或状态作授权依据。状态读取没有 SQL 写入、模型或 Task 调用。

## 全部实际修改文件（21）

| 用途 | 文件定位 |
| --- | --- |
| 协议及生成 | [agent.proto](../rpc/agent/agent.proto)、[agent.pb.go](../rpc/agent/pb/agent.pb.go)、[agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) |
| Agent | [main.go](../cmd/agent/main.go)、[server.go](../rpc/agent/server.go)、[trigger_status.go](../rpc/agent/trigger_status.go)、[trigger_status_store.go](../rpc/agent/trigger_status_store.go)、[trigger_status_test.go](../rpc/agent/trigger_status_test.go)、[trigger_status_store_test.go](../rpc/agent/trigger_status_store_test.go) |
| Gateway | [main.go](../api/main.go)、[agent_trigger_status.go](../api/agent_trigger_status.go)、[agent_trigger_status_test.go](../api/agent_trigger_status_test.go) |
| 页面 | [chat.html](../examples/chat.html)、[chat.test.cjs](../examples/chat.test.cjs)、[multi-draft-view.js](../examples/multi-draft-view.js)、[multi-draft-view.test.cjs](../examples/multi-draft-view.test.cjs) |
| 设计与审查 | [architecture-decisions.md](architecture-decisions.md)、[project-plan.md](project-plan.md)、[trigger-status-contract.md](trigger-status-contract.md)、[trigger-status-review.md](trigger-status-review.md)、[worktree-collaboration-plan.md](worktree-collaboration-plan.md) |

## 验证与界限

- 共同协议使用本机已有 protobuf 工具生成，`go test ./rpc/agent -run '^$'` 编译通过。三分支合入后，Agent/进程状态定向测试与 Gateway 定向测试通过；最终 `go test ./...` 全仓通过。
- `node --test examples/chat.test.cjs examples/multi-draft-view.test.cjs`：169项通过，覆盖本人/非本人、历史/实时消息、无效命令和大 ID、四状态、非法回包、旧身份/群响应与未保存审查冲突。`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...` 通过；`git diff --check` 通过。
- Agent 的来源、User/IM 鉴权、SQL 存储和 Gateway/页面主要由替身分别验证；这些结果不等于真实 MySQL、Kafka、生产 mTLS、浏览器与完整进程联调。未执行 022—026 迁移，未请求真实方舟模型或部署/推送云端。默认关闭 worker 时状态接口不可用；实际开启、通知重放及进程崩溃恢复的端到端验收仍在阶段6后续步骤。页面上的按钮只是入口，最终权限始终由服务端判断。
