# 多项草稿保存与读取：本批审查

2026-10-04。按已确认 A37/A50/A54—A56；[共同契约](multi-draft-storage-contract.md)。上一批模型与核验准备 `5ef2ff8` 已合入 main，本批共同协议基线 `f77091e`，工作区 `codex/multi-draft-storage-integration`。本批代码整合提交 `6de5244` 已通过下述验证，main 保留上一批，供用户审查。

## 八个小步骤

| 步骤 | 目的和范围 | 交付状态 |
| --- | --- | --- |
| 1 共同契约 | 明确集合模式、稳定索引、项状态、显式项身份；准备协议、生成代码、019 初始化/迁移 | 已准备，现有 Agent/Gateway 测试通过；当时新增 RPC 仍未实现 |
| 2 生成和保存 | 授权和同键重放优先；一次模型生成，所有项核验后在同一事务保存 | 已接线，9参数运行/19参数逐项 INSERT；回滚、同键赢家和指纹冲突替身通过 |
| 3 集合及指定项读取 | 限本人且复查当前团队群资格，完整验证数量、范围、字段和项索引 | 本机 RPC/SQL 替身通过，LEFT JOIN 缺项、超5项、坏字段均拒绝；第0/第1项显式读取 |
| 4 旧入口保护 | 即使集合只有一项，也不能从旧单项接口读写/确认/回帖 | 原读/锁 SELECT 加模式 CASE；旧编辑锁也保护；单项旧指纹/键/字段布局保留 |
| 5 Gateway 生成 | 新集合 POST，复用原 Token、请求键和首次指令参考 | 新路由22秒预算，原body验证共用；成功返回原运行字符串ID |
| 6 Gateway 读取 | 新集合 GET 和显式项 GET；保留精确大整数，拒绝损坏 RPC 成功结果 | 新路由15秒预算，完整shape检查；模式不符409、依赖不可用503、坏成功结果502 |
| 7 跨层组合 | 实际 HTTP/TCP gRPC 生产处理器配 SQL、身份、IM、模型替身，核对全部项实写实读 | 两个新组合测试函数通过，覆盖两项独立依据、重放/冲突、六种旧操作拒绝、撤权及坏后项 |
| 8 集中审查 | 核对允许文件、合入整合分支、运行回归、更新完整文件清单 | 无冲突整合；全量Go、Node140、Linux Agent/Gateway编译通过 |

## 调用链

```text
集合 POST → Gateway → Agent PrepareTaskDraftCollection
  → User 当前身份 → IM 当前团队群授权消息 → 查本人同键
  → 已有相同请求返回原 run；不同模式/内容/参考冲突
  → Eino GenerateDrafts → 每项来源/真实成员/有限时间核验
  → 一个事务保存 run(collection,N) 和稳定索引 0..N-1 的全部草稿

集合/项 GET → Gateway → Agent 当前身份 → 本人持久记录
  → 验证集合完整性 → IM 当前团队群资格 → 完整/指定项响应
```

生成成功只表示草稿已保存，全部项仍待本人确认，没有创建任务或发群卡片。新集合不能通过旧单项入口操作。本批不修改页面，不实现多项编辑、确认、跳过、回帖或群内触发；这些按已确认设计后续逐批接线。

## 验证与实际文件

后端提交 `b5ca4af`（13文件），Gateway `e268d38`（4文件），组合测试 `a61e058`（1文件）。主 agent 先快进后端，再合 Gateway `9528a15` 和组合 `6de5244`，无冲突。三个分支均从 `f77091e` 出发；执行 agent 只编辑、gofmt 与 diff 检查，由主 agent 集中执行测试。后端开始 UTC16:58:41，约12分钟完成；组合角色可见时钟 UTC17:04:15—17:11:07，前期读契约早于该窗口。没有完整同任务串行对照，不推断三倍提速。

实际验证：

- 协议准备时 `go test ./rpc/agent ./cmd/agent ./api -count=1` 通过，只证明准备阶段兼容。
- Gateway worktree `go test ./api -count=1`、后端 worktree `go test ./rpc/agent ./cmd/agent -count=1` 通过。
- 最终整合 `go test ./... -count=1` 全量通过，含后端最后追加的无项/索引检查和两项实际HTTP/TCP gRPC组合。User、IM、SQL和Eino模型仍为替身；未运行生产服务进程。
- `node --test examples/chat.test.cjs` 140/140 通过；本批未修改页面，仅验证已有单项和聊天页面回归。
- `CGO_ENABLED=0 GOOS=linux go build` 分别编译 `./cmd/agent` 和 `./api` 通过；没有构建或启动 Docker 容器。
- gofmt、差异检查通过，执行 worktree 交付提交后保持干净；原生成文件正常再生成，未删除。

全部 **33个实际修改文件**（相对于 main `5ef2ff8`，含协议生成、测试和文档）：

| 文件 | 实际作用 |
| --- | --- |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 三个集合/指定项方法，显式optional项索引与响应 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 正常再生成protobuf类型 |
| [rpc/agent/pb/agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) | 正常再生成gRPC桩 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | 新库mode/count/项状态列 |
| [019_agent_draft_collection.sql](../deploy/mysql/migrations/019_agent_draft_collection.sql) | 已有库增量列，仅准备 |
| [draft_collection_prepare.go](../rpc/agent/draft_collection_prepare.go) | 模式指纹、重放、一次生成与完整核验 |
| [draft_prepare_rpc.go](../rpc/agent/draft_prepare_rpc.go) | 新集合prepare RPC接线 |
| [draft_collection_store.go](../rpc/agent/draft_collection_store.go) | 原子保存及完整持久集合校验 |
| [draft_collection_access.go](../rpc/agent/draft_collection_access.go) | 本人读取与当前群范围授权 |
| [draft_collection_rpc.go](../rpc/agent/draft_collection_rpc.go) | 集合及显式项读取RPC响应 |
| [draft_store.go](../rpc/agent/draft_store.go) | 旧读取及文字编辑锁的模式保护 |
| [draft_confirm_store.go](../rpc/agent/draft_confirm_store.go) | 旧freeze/结果锁拒绝collection |
| [draft_collection_prepare_test.go](../rpc/agent/draft_collection_prepare_test.go) | 指纹、重放优先、坏后项/权限 |
| [draft_collection_store_test.go](../rpc/agent/draft_collection_store_test.go) | SQL原子性、赢家、损坏集合、旧锁保护 |
| [draft_collection_rpc_test.go](../rpc/agent/draft_collection_rpc_test.go) | 显式索引及当前资格 |
| [draft_confirm_store_test.go](../rpc/agent/draft_confirm_store_test.go) | 原测试仅适配CASE查询 |
| [draft_edit_test.go](../rpc/agent/draft_edit_test.go) | 原测试仅适配CASE查询 |
| [draft_revision_test.go](../rpc/agent/draft_revision_test.go) | 原测试仅适配CASE查询 |
| [api/agent_draft_collection.go](../api/agent_draft_collection.go) | 三个HTTP handlers与完整响应校验 |
| [api/agent_draft_collection_test.go](../api/agent_draft_collection_test.go) | 10个函数覆盖非法输入/服务结果及边界 |
| [api/agent_draft.go](../api/agent_draft.go) | 生成/输出小helper复用，FailedPrecondition409与Unavailable503分开 |
| [api/main.go](../api/main.go) | 三个新路由与既有预算 |
| [api/multi_draft_persistence_flow_test.go](../api/multi_draft_persistence_flow_test.go) | 真实HTTP/TCP gRPC生产链的组合验证 |
| [docs/multi-draft-storage-contract.md](multi-draft-storage-contract.md) | 共同字段/SQL/权限/兼容与角色契约 |
| [docs/multi-draft-storage-review.md](multi-draft-storage-review.md) | 八步、全部文件、实际验证与限制 |
| [docs/architecture-decisions.md](architecture-decisions.md) | 已定A方案实施取舍、代价及验证记录 |
| [docs/project-plan.md](project-plan.md) | 最新进度、合入记录、后续任务及迁移依赖 |
| [docs/agent-multi-draft-design.md](agent-multi-draft-design.md) | 前置批次合入状态与本批审查关联 |
| [docs/stage6-acceptance.md](stage6-acceptance.md) | 新集合本机验证和未执行真实验收 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | 三角色本批目录/分支/允许范围与调度记录 |
| [rpc/agent/README.md](../rpc/agent/README.md) | 集合RPC、生产配置复用及模式兼容 |
| [api/README.md](../api/README.md) | 新HTTP路径、数据字段及错误语义 |
| [deploy/README.md](../deploy/README.md) | 019迁移依赖与协调升级说明 |

真实 MySQL 迁移、锁和唯一约束、真实方舟模型、浏览器、容器及云部署未验证。019 脚本只准备，最终在 018 后、更新 Agent 前核对执行；修改 init.sql 不会升级已有数据卷。未推送远程、未执行迁移或部署。
后续主分支记录（2026-10-04）：用户继续要求下一步后，主agent将上轮已审查的最终提交c24dc97快进合入main，无冲突，业务代码与本页通过版本相同。三个worktree保留，未push/部署/执行迁移。新批次逐项编辑按[契约](multi-draft-edit-contract.md)实施；上方main旧版本是上轮交付时历史。
