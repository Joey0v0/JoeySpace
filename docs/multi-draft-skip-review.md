# 多项草稿本人跳过审查

2026-10-04。沿已确认A50/A56和[共同契约](multi-draft-skip-contract.md)。上轮完整91ff7ef已快进合入本地main，本批共同提交8c07f7e已通过Agent/Gateway旧测试。三个独立worktree后端/Gateway/组合并行，root负责协议/generated/docs、统一提交与集中验证。后端20bf871、Gatewaybbb3fcf、组合ba0e33c按顺序无冲突整合，业务代码e4163fd通过下列验证。本轮codex/multi-draft-skip-integration供审查，main仍91ff7ef，不标阶段6全部完成。

## 七步、目的和调用链

1. 共同协议：新增SkipTaskDraftItem，明确run/index/内容版本、终态和原记录保留，生成代码正常再生成。解决缺项身份或旧内容被盲跳过。
2. 后端跳过：User确认本人、IM当前群资格，授权后短事务完整锁读，只更新待确认目标状态；同版本已跳过重放不UPDATE，无模型、目标成员、Task或回帖调用。
3. 后端状态保护：GET保留合法跳过项/原证据，编辑和确认拒绝终态；跳过不撤销已冻结操作、内容版本不增，其他waiting仍可编辑创建。
4. Gateway入口：POST指定项skip，仅接受规范正expected_revision字符串，原Bearer和显式index转发，15秒路由。
5. Gateway结果：返回须准确项身份/skipped/Task0/原版本/disabled空消息/完整证据，假成功拒绝；读取可保留未处理重名或模糊时间，创建中/成功项保护不变。
6. 组合验证：实际本机HTTP/TCP gRPC生产Agent和存储适配、SQL/User/IM/Task替身，覆盖版本、权限、重放、并发状态竞争和其他项独立操作。
7. 集中审查：root检查允许文件、后端→Gateway→组合整合、集中Go/页面/编译验证，填写全部实际文件供用户审查。

`本人跳过某项 → Gateway → Agent → User本人 / IM当前群权限 → Agent短事务核对完整目标、版本与未提交状态 → 保存skipped → 返回原草稿与版本`。

跳过与确认争用同一短集合锁：先跳过则后确认不能创建；先冻结则跳过失败，仍由本人重读后明确重试原创建。事务内不调用外部RPC，不提供跨服务授权原子保证。SQL只更新目标status，不删除/重排/清空证据或修改其他项。

## 边界与验证

本轮不接页面跳过按钮、进度汇总或逐项群回帖，返回逐项事实。已跳过不能恢复编辑/创建，不代表取消Task；所有项已创建/跳过只代表候选处理完，全部跳过不能宣称已创建任务或回帖已送达。无新增迁移/依赖，019与前置迁移、真实MySQL/模型/浏览器/容器及云端仍待最终验收。

实际执行验证：共同协议 `go test ./rpc/agent ./api -count=1` 通过；Gateway worktree `go test ./api -count=1`、后端worktree `go test ./rpc/agent ./cmd/agent -count=1` 均通过。整合e4163fd的 `go test ./... -count=1` 全量通过，包含本批三组实际HTTP/TCP gRPC组合与原单项、多项确认/编辑回归。`node --test examples/chat.test.cjs` 140/140通过，本轮未改页面，仅回归既有操作。`CGO_ENABLED=0 GOOS=linux go build` Agent与Gateway通过，未启动容器；生成代码正常再生成，gofmt与diff检查通过，合并换行规范不改变业务代码。

后端新增11个测试函数（6 SQL/5 RPC）、Gateway7个、组合3个，数量不是完成率。验证原歧义依据和最大版本保留，同版本重放无UPDATE，0/2行与提交失败不报告成功，锁内同版本内容变化/版本变化/范围变化拒绝，其他项变化不冲突，跳过项禁止确认/编辑且回复disabled。组合测试用SQL返回模拟读取后、取得锁前另一操作先推进状态，两向竞争均拒绝错误操作且不调用Task；没有真实MySQL并发/崩溃验收。真实传输是本机HTTP/TCP gRPC，SQL/User/IM/Task仍为替身，无真实模型请求、数据库迁移、浏览器或云端操作。

全部 **28个实际修改文件**（相对于本地main91ff7ef，含协议生成、测试和文档）：

| 文件 | 实际作用 |
| --- | --- |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 新skip RPC和显式项身份/版本 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 正常生成请求类型 |
| [rpc/agent/pb/agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) | 正常生成RPC桩 |
| [draft_collection_skip.go](../rpc/agent/draft_collection_skip.go) | 本人/当前群权限、目标版本和可跳过状态编排 |
| [draft_collection_skip_store.go](../rpc/agent/draft_collection_skip_store.go) | 完整集合短锁、目标限定UPDATE、幂等与竞争保护 |
| [draft_collection_skip_rpc.go](../rpc/agent/draft_collection_skip_rpc.go) | RPC身份/预算和原项返回，复用Access配置 |
| [draft_collection_skip_store_test.go](../rpc/agent/draft_collection_skip_store_test.go) | 歧义/MAX/重放、锁竞争、回滚、另项继续操作 |
| [draft_collection_skip_rpc_test.go](../rpc/agent/draft_collection_skip_rpc_test.go) | 仅Access配置、当前权限/版本、无成员/Task查询、终态保护 |
| [draft_collection_confirm.go](../rpc/agent/draft_collection_confirm.go) | skipped空Task键/ID0合法读取 |
| [draft_collection_confirm_rpc.go](../rpc/agent/draft_collection_confirm_rpc.go) | 跳过项确认提前拒绝，旧冻结进度保护继续复用 |
| [draft_collection_store.go](../rpc/agent/draft_collection_store.go) | skipped编辑目标拒绝，完整校验保留 |
| [draft_collection_rpc.go](../rpc/agent/draft_collection_rpc.go) | skipped回复固定disabled，不调用旧回帖 |
| [api/agent_draft_collection_skip.go](../api/agent_draft_collection_skip.go) | skip POST解析、原Token转发、准确结果检查 |
| [api/agent_draft_collection_skip_test.go](../api/agent_draft_collection_skip_test.go) | 身份/字段/大版本/异常结果/错误映射 |
| [api/agent_draft_collection.go](../api/agent_draft_collection.go) | 合法skipped读取及回复/依据检查 |
| [api/agent_draft_collection_test.go](../api/agent_draft_collection_test.go) | 混合读取与跳过歧义允许，未知/损坏拒绝 |
| [api/agent_draft_collection_edit_test.go](../api/agent_draft_collection_edit_test.go) | skipped不能冒充编辑成功 |
| [api/agent_draft_collection_confirm_test.go](../api/agent_draft_collection_confirm_test.go) | skipped不能冒充创建成功 |
| [api/main.go](../api/main.go) | 新15秒skip路由 |
| [api/multi_draft_skip_flow_test.go](../api/multi_draft_skip_flow_test.go) | 三组实际HTTP/TCP gRPC生产链与SQL/业务替身 |
| [api/README.md](../api/README.md) | HTTP示例、原版本和终态含义 |
| [rpc/agent/README.md](../rpc/agent/README.md) | RPC/状态/事务与配置复用说明 |
| [docs/multi-draft-skip-contract.md](multi-draft-skip-contract.md) | 共同契约与明确工作范围 |
| [docs/multi-draft-skip-review.md](multi-draft-skip-review.md) | 七步、全部28文件与验证局限 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A50/A56内版本/状态/重放取舍、备选与代价 |
| [docs/project-plan.md](project-plan.md) | 最新真实能力和阶段6剩余链路 |
| [docs/stage6-acceptance.md](stage6-acceptance.md) | 实际本地验证及待执行真实验收 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | 同一基线/三worktree角色与整合记录 |
