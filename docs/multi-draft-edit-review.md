# 多项草稿逐项编辑审查

2026-10-04。沿已确认A50/A54，依据[共同契约](multi-draft-edit-contract.md)。上轮保存/读取c24dc97已合main，本批共同提交01dedbf，整合分支codex/multi-draft-edit-integration。整合业务代码a15ac57已通过全量回归。仅逐项文字、负责人和截止时间编辑；任务创建、跳过、回帖与页面留后续步骤，不把协议准备当业务完成。

## 八步与目的

| 步骤 | 改动解决的问题 | 状态 |
| --- | --- | --- |
| 1 共同协议 | 三种写RPC显式绑定run/index/revision，明确HTTP body与事务验证；原模式保护不改 | 准备测试通过，新方法当时仍Unimplemented |
| 2 文字编辑 | 指定项标题/说明可单独保存，其他项版本不变 | RPC与共享短事务已接线，文本trim/版本/完整目标比较通过 |
| 3 负责人选择 | 重名等情况由本人选当前成员或明确未指派，原称呼保留 | 正ID当前User资格、0无需目标查询，selected/unassigned同值状态变更通过 |
| 4 截止时间编辑 | 模糊表达本人补时间或明确不设；保留原文/来源/参考 | due及resolution更新，其余原证据不改，needs_input0→unset和no-op通过 |
| 5 Gateway请求 | 三条PUT验证明确项身份/字段/正版本，转发原Token | 15秒路由与严格正文/规范ID/显式零/Token转发测试通过 |
| 6 保存结果验证 | 避免错项、错内容、异常版本或缺依据被报告成功 | 复用集合item校验，错身份/缺依据/错值/跳变版本502，冲突409测试通过 |
| 7 跨层组合 | 实际HTTP/TCP gRPC配生产Agent，验证目标实写、版本冲突和非目标不变 | 三个新组合测试通过，确切SQL参数、独立版本/原依据、更新0/2行回滚、撤权/旧模式保护 |
| 8 集中审查 | 按允许文件整合、回归、构建并记录全部文件及限制 | 全量Go、Node140及LinuxAgent/Gateway编译通过，无冲突整合 |

## 调用链与版本含义

```text
PUT 指定项 → Gateway规范run/index、正文、版本与Token
  → Agent确定本人 → 读完整collection → IM核对当前群权限
  → 核对目标项版本，正负责人再调User查当前成员
  → 短事务完整集合FOR UPDATE，再核对范围/数量及目标完整草稿/版本
  → 仅更新指定项，WHERE run/index/revision/waiting，变化时版本+1
  → 返回事务内实际保存项 → Gateway核对身份、版本、提交值与完整依据
```

各项版本独立，修改第1项不增加第0项版本；事务可能短暂串行同轮写入，不等于整个运行共用版本。相同内容/同处理状态no-op仍核对但不UPDATE；从歧义状态明确选择未指派或不设时间属于实际审查，数值同为0也加版本。版本耗尽只拒绝实际变化，不阻止no-op。

所有外部权限RPC在事务前，沿既有授权规则，不声称跨服务权限与SQL原子。没有模型调用、Task创建或群消息副作用。原时间解释依据和原称呼保留，文字/负责人/时间三类保存不夹带修改其他字段。

## 实际交付与验证

后端3c4988c（7文件）、Gateway d074707（3文件）、组合ae42ae4（1文件），由主agent逐个审查允许文件并提交；后端快进→Gateway合并692d29e→组合合并a15ac57，无冲突。三个agent只编辑/gofmt/diffcheck，不自行提交/合main/push、不运行Go测试或请求测试审批；全部测试由主agent集中执行。后端UTC17:34:13—约17:47，约13分钟；组合UTC17:34:59—17:42:09，约7分10秒。没有同任务单agent对照，不推断固定倍数提速。三个worktree保留且交付后干净，main仍c24dc97，本批整合结果待用户审查。

实际执行验证：

- 共同协议准备 `go test ./rpc/agent ./cmd/agent ./api -count=1` 通过；当时新增写方法仍Unimplemented。
- Gateway worktree `go test ./api -count=1`、后端 worktree `go test ./rpc/agent ./cmd/agent -count=1` 通过。
- 最终整合 `go test ./... -count=1` 全量通过，含所有旧单项、多项保存/读取与本批RPC/SQL/HTTP组合回归。
- `node --test examples/chat.test.cjs` 140/140通过，本批未改页面，仅回归已有聊天和单项流程。
- `CGO_ENABLED=0 GOOS=linux go build`分别编译`./cmd/agent`和`./api`通过，未构建/启动容器。
- gofmt、diff检查通过；协议生成文件正常再生成，没有删除文件或改依赖/迁移。

RPC/SQL定向共七个新测试函数，Gateway七个，跨层三个；测试数量不是功能完成率。SQL替身核对FOR UPDATE与确切run/index/revision/waiting UPDATE，原证据和其他项完整保留；不等于真实MySQL并发/锁验收。已知目标creating/succeeded/skipped在内部编辑加载中拒绝409，未知/损坏仍Unavailable；普通集合GET仍保留上轮严格待确认约束，没有提供新创建/跳过状态入口。

全部 **23个实际修改文件**（相对于main c24dc97，含协议生成、测试和文档）：

| 文件 | 实际作用 |
| --- | --- |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 三种指定项编辑RPC及optional身份/显式值 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 正常再生成请求/响应类型 |
| [rpc/agent/pb/agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) | 正常再生成RPC桩 |
| [draft_collection_edit.go](../rpc/agent/draft_collection_edit.go) | 本人项编排、当前资格、目标版本与成员核对 |
| [draft_collection_edit_store.go](../rpc/agent/draft_collection_edit_store.go) | 共用短事务、允许字段、三UPDATE、no-op与独立项版本 |
| [draft_collection_edit_rpc.go](../rpc/agent/draft_collection_edit_rpc.go) | 三RPC输入/预算/准确保存项响应 |
| [draft_collection_access.go](../rpc/agent/draft_collection_access.go) | 既有授权复用，编辑目标内部读取接线 |
| [draft_collection_store.go](../rpc/agent/draft_collection_store.go) | 共享完整查询/锁校验，已知目标不可编辑分类 |
| [draft_collection_edit_store_test.go](../rpc/agent/draft_collection_edit_store_test.go) | 原证据/非目标、状态同值、MAX/no-op、锁变更/回滚 |
| [draft_collection_edit_rpc_test.go](../rpc/agent/draft_collection_edit_rpc_test.go) | 本机RPC、三操作、当前权限/成员、缺字段与过时版本 |
| [api/agent_draft_collection_edit.go](../api/agent_draft_collection_edit.go) | 三PUT解析/转发、保存值与完整项结果校验 |
| [api/agent_draft_collection_edit_test.go](../api/agent_draft_collection_edit_test.go) | 七测试及输入/Unicode/版本/身份/假成功边界 |
| [api/main.go](../api/main.go) | 三条15秒预算PUT路由 |
| [api/multi_draft_edit_flow_test.go](../api/multi_draft_edit_flow_test.go) | 三个实际HTTP/TCP gRPC生产编辑链组合测试 |
| [docs/multi-draft-edit-contract.md](multi-draft-edit-contract.md) | 固定协议/HTTP/事务与角色范围 |
| [docs/multi-draft-edit-review.md](multi-draft-edit-review.md) | 八步、全部23文件及实际验证边界 |
| [docs/architecture-decisions.md](architecture-decisions.md) | 既定A方案下请求与锁取舍、代价和实际验证 |
| [docs/project-plan.md](project-plan.md) | 最新成果及剩余多项链路 |
| [docs/multi-draft-storage-review.md](multi-draft-storage-review.md) | 上轮审查成果快进合main历史记录 |
| [docs/stage6-acceptance.md](stage6-acceptance.md) | 本批可用接口与尚未执行真实验收 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | 共基线三角色目录/分支/范围与集成记录 |
| [rpc/agent/README.md](../rpc/agent/README.md) | 三类RPC编辑及版本/权限/锁语义 |
| [api/README.md](../api/README.md) | 三PUT正文、返回与显式审查/no-op规则 |

真实MySQL锁/唯一约束/事务与并发、浏览器、方舟模型、容器和云端未验收。本批没有新迁移，仍依赖此前019且未执行真实脚本；当前页面走旧单项接口，多项页面尚未接入。
