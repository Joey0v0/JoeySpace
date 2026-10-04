# 多项草稿逐项确认审查

2026-10-04。沿[共同契约](multi-draft-confirm-contract.md)和已确认A41/A50/A54。上轮逐项编辑4700612已快进合入本地main；本批共同起点fbe19f7，三个独立worktree分别负责后端、Gateway、组合验证，主agent统一生成协议、审查、运行测试及集成。后端1b043b1、Gateway3fca606、组合5268223按顺序无冲突整合，业务代码00bc208通过以下全量验证。本批在codex/multi-draft-confirm-integration供用户审查，main保留4700612。

## 八个小步骤

1. 共同协议：新增ConfirmTaskDraftItem，要求显式项索引、内容版本和完整已审查快照。原single接口和键保持，生成代码正常重新生成，没有删除已有协议。
2. 后端逐项冻结：授权后短事务校验完整集合，锁定并冻结目标项与固定Task请求键，提交后才允许调用Task。解决并发编辑或重复确认导致提交不同内容。
3. 后端创建及保存：复用现有Task RPC、原Token和固定项键；成功保存正TaskID，不确定保持creating，本人重读后显式重试。成功重放复用结果，避免重复任务。
4. 后端混合状态：集合可同时含waiting/creating/succeeded，严格检查每项状态与键/ID。其他项冻结不妨碍待确认项编辑，目标冻结拒绝改动，内容版本不随冻结/结果变化。
5. Gateway确认入口：新增指定项POST，严格要求完整审查字段，校验身份/规范ID/索引并转发原Token。旧单项入口保持。
6. Gateway结果检查：成功必须succeeded、正TaskID、正确范围/项身份以及与本人快照完全一致；集合读取支持合法混合状态，编辑返回仍必须waiting/task0。
7. 组合验证：实际本机HTTP/TCP gRPC、生产Agent/存储编排，SQL/User/IM/Task替身模拟冻结、异常、重试与不同项。不能将替身测试当真实环境验收。
8. 主agent审查回归：核对文件边界和旧接口兼容，后端→Gateway→组合在本地整合分支合并，集中测试与文档记录，交付用户审查。

## 调用链与边界

`本人确认指定项 → Gateway → Agent → User本人/IM当前群资格 → Agent短事务冻结 → Task CreateTask → Agent短事务保存该项结果 → Gateway返回任务ID`。

Task RPC不在SQL事务内，不确定时不解冻、不重新生成、不自动后台执行。集合运行头保持waiting，各项状态独立权威；没有跨服务事务或整轮全成功承诺。草稿生成/编辑/读取仍属Agent，任务仍属Task，Gateway不直接写库。

本轮新增的多项确认不调用旧run级机器人回帖；disabled/not_started不表示IM受理或群成员送达。逐项回帖、显式跳过、多项页面和群内@AI待后续批次；原single完整链保持。无新增迁移或依赖，019及此前迁移未执行真实环境，方舟接入点/预算仍待准备。

## 验证与全部文件

实际执行：共同协议 `go test ./rpc/agent ./api` 通过；Gateway worktree `go test ./api -count=1`、后端worktree `go test ./rpc/agent ./cmd/agent -count=1` 均通过。整合业务代码00bc208的 `go test ./... -count=1` 全量通过，含三项实际HTTP/TCP gRPC组合及旧single链。`node --test examples/chat.test.cjs` 140/140通过；本轮未改页面，只回归既有操作。`CGO_ENABLED=0 GOOS=linux go build` 分别构建 `./cmd/agent` 和 `./api` 成功，未启动容器。生成代码正常再生成，根工作区合并后统一gofmt规范换行、diff检查通过，收尾只修改文档。

后端新增12个测试函数（7 SQL/5 RPC）、Gateway10个、组合3个，数量不表示功能完成率。组合Task/User/IM/SQL为替身，真实本机传输和生产Agent编排/存储适配已跑；Task收到同项原键并保持稳定结果，不等于真实MySQL并发、容器或模型效果已验收。Task回报DeadlineExceeded与Unavailable模拟不确定；没有真实云断网或进程崩溃演练。SQL错误和影响0/2行回滚，不报告成功；结果保存失败后同键恢复，成功重放不再调用Task。没有真实执行019，阶段6不标全部完成。

全部 **26个实际修改文件**（相对于本地main4700612，包括测试、生成代码与文档）：

| 文件 | 实际作用 |
| --- | --- |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 指定项确认RPC和完整审查请求 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 正常生成新请求类型 |
| [rpc/agent/pb/agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) | 正常生成新RPC桩 |
| [draft_collection_confirm.go](../rpc/agent/draft_collection_confirm.go) | 稳定项键、合法状态/审查与同内容状态推进 |
| [draft_collection_confirm_store.go](../rpc/agent/draft_collection_confirm_store.go) | 两个短事务、目标冻结和结果独立保存 |
| [draft_collection_confirm_rpc.go](../rpc/agent/draft_collection_confirm_rpc.go) | 当前权限、Task调用、重试和既有生产配置复用 |
| [draft_collection_confirm_store_test.go](../rpc/agent/draft_collection_confirm_store_test.go) | 独立键/版本、并发状态、回滚、异结果、混合状态与损坏 |
| [draft_collection_confirm_rpc_test.go](../rpc/agent/draft_collection_confirm_rpc_test.go) | 不确定重试、权限/快照、失败保护、成功重放、不调用旧回帖 |
| [draft_collection_store.go](../rpc/agent/draft_collection_store.go) | 共用读取/锁校验支持合法混合项状态 |
| [draft_collection_edit_store_test.go](../rpc/agent/draft_collection_edit_store_test.go) | 冻结fixture补合法键及已处理依据，原409/损坏断言保留 |
| [api/agent_draft_collection_confirm.go](../api/agent_draft_collection_confirm.go) | 完整HTTP审查输入、准确结果与Task冲突错误 |
| [api/agent_draft_collection_confirm_test.go](../api/agent_draft_collection_confirm_test.go) | 必填/身份/Unicode/错误/异常快照保护 |
| [api/agent_draft_collection.go](../api/agent_draft_collection.go) | 三种状态及结果、未审查伪冻结响应检查 |
| [api/agent_draft_collection_test.go](../api/agent_draft_collection_test.go) | 混合读取、状态/结果一致性、伪冻结拒绝 |
| [api/agent_draft_collection_edit.go](../api/agent_draft_collection_edit.go) | 编辑返回继续严格waiting/task0 |
| [api/agent_draft_collection_edit_test.go](../api/agent_draft_collection_edit_test.go) | 合法冻结响应也不能冒充保存编辑成功 |
| [api/main.go](../api/main.go) | 20秒确认POST路由 |
| [api/multi_draft_confirm_flow_test.go](../api/multi_draft_confirm_flow_test.go) | 三项实际HTTP/TCP gRPC组合验证 |
| [api/README.md](../api/README.md) | HTTP示例、状态和重试边界 |
| [rpc/agent/README.md](../rpc/agent/README.md) | 生产配置复用、短事务及固定项键说明 |
| [docs/multi-draft-confirm-contract.md](multi-draft-confirm-contract.md) | 共同协议、SQL、状态、错误与工作范围 |
| [docs/multi-draft-confirm-review.md](multi-draft-confirm-review.md) | 八步、26文件、实际验证和局限 |
| [docs/architecture-decisions.md](architecture-decisions.md) | 已定A方案内状态/键/事务取舍及代价 |
| [docs/project-plan.md](project-plan.md) | 本批实际能力与阶段6剩余项 |
| [docs/stage6-acceptance.md](stage6-acceptance.md) | 本地结果和待执行真实验收场景 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | 同一基线、三角色目录/范围/集成记录 |
