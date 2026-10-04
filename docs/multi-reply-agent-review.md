# Agent 逐项回帖闭环审查

2026-10-04。沿用户已确认 A37/A41/A44/A46/A47/A50/A55 和[共同契约](multi-reply-agent-contract.md)。上轮完整1a1156b已快进合本地main；共同352f446统一项协议、Agent表021及兼容契约。三个worktree完成存储406d1d2、编排1be0e98、Gateway9e7de80，root顺序审查整合，追加组合与测试修正为业务5732006。当前codex/multi-reply-agent-integration供审查，main仍1a1156b；未push、执行迁移或云同步，三个worktree保留。

## 九步实际成果

1. 统一接口和表：新增专用逐项重试RPC，要求显式索引；021扩Agent表主键为(run,index)，旧记录默认0、原消息唯一键保持，015不改。解决一轮多项共用回帖记录和旧接口误认第0项的问题。
2. 固定项意图：只从已保存成功项生成卡片，固定run/index/Task/群范围/正文/消息ID，公共helper第0项沿旧ID；原单项查询/受理只访问第0项。解决串项和覆盖旧依据的问题。
3. 持久保护：短事务完整读集合，只比较目标完整内容/版本/结果；prepare/save/load/accept严格核对记录、行数和提交结果。其他项合法变化不冲突，完整accept按字节匹配MsgID/正文，读操作不写入。
4. 确认后同步回帖：Task结果保存后才保存固定意图并调用IM专用项方法，精确accepted响应后保存本地受理；沿18秒总预算、12秒创建、5秒回帖、4秒IM子调用。失败保留任务成功和pending/unknown，不重建Task。
5. 本人重试与只读事实：新增RetryTaskReplyItem；每次重新确认本人/当前群资格，仅成功目标重试固定卡片。起始已成功的重复确认只读，GET无发送动作；集合查成功项、指定项只查目标，故障不伪受理。
6. Gateway入口：新增指定项POST回帖重试，19秒路由预算；只转发run/index和原Bearer，空body/query，拒绝调用者替换TaskID或正文。错误脱敏，冲突409、当前资格403。
7. 结果验证：共享校验绑定消息ID到run/index，pending/accepted/unknown仅成功项，跳过固定disabled；新重试只接受精确目标succeeded/accepted。旧文字/版本/负责人/时间审查约束和ID字符串精度保留。
8. 本机组合：新增三组实际HTTP/TCP gRPC/mTLS测试，生产Agent/存储适配和Agent机器人客户端，配合业务/SQL替身覆盖独立发送、显式恢复和权限撤销。
9. 集中审查：root统一提交/整合/测试和文档，所有子agent仅编辑/格式/diff检查。修正测试把合法旧0键误设为错误键的fixture，删除组合测试无用import；没有因此放宽生产校验。完整文件如下。

`HTTP 逐项确认 → Agent 本人/当前群资格 → 目标冻结 → Task 原项键创建 → Agent 保存成功TaskID → 保存独立项卡片 → IMBot.PostTaskCreatedCardItem（mTLS＋原Token）→ 精确IM受理 → Agent保存accepted`。

`HTTP 指定项回帖重试 → 本人当前资格 → 原成功项/固定卡片 → IM 专用项方法 → 本地受理`，不调用Task、模型或目标负责人查询。GET只在授权后读取，重复成功确认不隐式发送。旧单项请求/Task键/消息ID保持。

## 实际验证与限制

共同准备 `go test ./rpc/agent ./api ./cmd/agent -count=1` 通过；整合后同定向验证通过。业务5732006的 `go test ./... -count=1` 全量通过；`node --test examples/chat.test.cjs` 140/140通过，页面没有本轮新增。`CGO_ENABLED=0 GOOS=linux go build` IM、Agent、Gateway通过；没有启动容器。Go格式与diff检查通过，合并的CRLF规范后Git没有新增业务差异，后续仅文档更新。

存储新增8个、编排7个、Gateway9个（新handler文件6、GET文件3）、根组合3个测试函数；共同扩展原schema测试。数量不作为项目完成率。存储替身覆盖不同项/旧0/大ID、目标内容及版本竞争、其他项合法变化、NULL/多行/写0或2行/提交失败、只读损坏、精确受理更新及幂等重读；编排覆盖独立项、IM/prepare/accept失败、权限撤销、未成功项拒绝、纯GET、不降级旧方法、index4和取消预算；Gateway覆盖原Token、空体/路径、私密错误、坏服务结果及冻结状态约束。

三组组合测试通过实际本机HTTP和TCP gRPC，IM专用连接使用临时测试证书和生产Agent客户端的双向TLS。User、IM群权限、Task、IMBot业务和SQL使用替身；本批不把机器人业务替身称为生产IM/Kafka验证（前批生产IM监听组合见multi-reply-im-review.md）。分别验证：第0项超时而第1项正常受理，重复确认纯读，accepted重试仍授权；IM受理但Agent保存失败只重发卡片；意图保存失败返回Task成功/unknown，GET得到无记录后本人恢复，读存储故障返回503。各步核对SQL预期、精确大ID/消息ID、Task调用次数和另一项不变。

pending表示固定意图已保存，但Agent尚未保存受理依据；accepted仅说明已收到精确IM受理并保存本地结果，不证明历史落库或成员已收到。unknown仅确认响应保留Task成功时表示无法确定回帖存储事实，消息ID为空，应重读；无记录GET返回not_started。至少一次重试仍可能重复同MsgID，沿现有客户端去重方案，不新增后台恢复。

021是待执行脚本，初始化与旧015加021结果已有静态检查；真实MySQL并发锁/DDL/唯一约束、Kafka/Push/历史链、部署证书、模型、浏览器、容器和云同步均未验收。新Agent前核对021，新IM前核对020及既有前置迁移，再协调Gateway升级；不修改Compose或依赖。多项页面/候选进度和群内@AI随后推进，阶段6不标全部完成。

## 全部实际修改文件

相对main1a1156b共 **33文件**。核心实现分存储、编排与HTTP；其余为必要协议生成、迁移、故障测试及审查/部署文档，无无关整理。

| 文件 | 实际用途 |
| --- | --- |
| [rpc/agent/agent.proto](../rpc/agent/agent.proto) | 专用RetryTaskReplyItem，复用显式项请求/响应 |
| [rpc/agent/pb/agent.pb.go](../rpc/agent/pb/agent.pb.go) | 正常再生成协议描述 |
| [rpc/agent/pb/agent_grpc.pb.go](../rpc/agent/pb/agent_grpc.pb.go) | 正常生成逐项重试客户端/服务桩 |
| [rpc/agent/draft_collection_reply_contract.go](../rpc/agent/draft_collection_reply_contract.go) | 共同逐项store和专用bot接口 |
| [rpc/agent/draft_reply_store.go](../rpc/agent/draft_reply_store.go) | 记录项字段和旧0读/受理保护 |
| [rpc/agent/draft_reply_store_test.go](../rpc/agent/draft_reply_store_test.go) | 015加021与新初始化结果检查 |
| [rpc/agent/draft_collection_reply_store.go](../rpc/agent/draft_collection_reply_store.go) | 固定意图、事务prepare、只读load、完整accept |
| [rpc/agent/draft_collection_reply_store_test.go](../rpc/agent/draft_collection_reply_store_test.go) | 隔离/冲突/损坏/提交与受理故障业务测试 |
| [rpc/agent/draft_reply_rpc.go](../rpc/agent/draft_reply_rpc.go) | 原配置接新项store/bot，不增加配置入口 |
| [rpc/agent/draft_collection_confirm_rpc.go](../rpc/agent/draft_collection_confirm_rpc.go) | Task保存后发送，故障保留Task成功，重放纯读 |
| [rpc/agent/draft_collection_rpc.go](../rpc/agent/draft_collection_rpc.go) | 集合/目标GET读取真实项回帖 |
| [rpc/agent/draft_collection_reply_rpc.go](../rpc/agent/draft_collection_reply_rpc.go) | 独立发送预算、受理核对、本人显式重试 |
| [rpc/agent/draft_collection_reply_rpc_test.go](../rpc/agent/draft_collection_reply_rpc_test.go) | 编排/重试/纯读/权限与失败独立性 |
| [api/agent_draft_collection_reply.go](../api/agent_draft_collection_reply.go) | 严格逐项HTTP重试 |
| [api/agent_draft_collection_reply_test.go](../api/agent_draft_collection_reply_test.go) | 原身份/坏输入/错误与结果、确认/冻结回归 |
| [api/agent_draft_collection.go](../api/agent_draft_collection.go) | 绑定run/index的共享回帖状态/MsgID校验 |
| [api/agent_draft_collection_test.go](../api/agent_draft_collection_test.go) | 混合状态和错运行/项/旧0消息拒绝 |
| [api/agent_draft_collection_confirm.go](../api/agent_draft_collection_confirm.go) | 共享校验传原run，保留完整快照保护 |
| [api/agent_draft_collection_edit.go](../api/agent_draft_collection_edit.go) | 共享校验传原run，编辑状态要求保持 |
| [api/agent_draft_collection_skip.go](../api/agent_draft_collection_skip.go) | 共享校验传原run，跳过要求保持 |
| [api/main.go](../api/main.go) | 新19秒重试路由 |
| [api/multi_draft_reply_flow_test.go](../api/multi_draft_reply_flow_test.go) | 三组实际HTTP/TCP gRPC/mTLS故障恢复组合 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | Agent新库默认项0/组合主键 |
| [deploy/mysql/migrations/021_agent_task_reply_items.sql](../deploy/mysql/migrations/021_agent_task_reply_items.sql) | Agent旧库增加项列与主键，不删数据 |
| [rpc/agent/README.md](../rpc/agent/README.md) | 发送/重试/状态、021与兼容限制 |
| [api/README.md](../api/README.md) | HTTP使用、字段/状态/错误与升级说明 |
| [deploy/README.md](../deploy/README.md) | IM020/Agent021及协调升级前置 |
| [docs/multi-reply-agent-contract.md](../docs/multi-reply-agent-contract.md) | 共同接口、数据、分工和九步 |
| [docs/multi-reply-agent-review.md](../docs/multi-reply-agent-review.md) | 本文：全部实际文件、成果与限制 |
| [docs/architecture-decisions.md](../docs/architecture-decisions.md) | A55实施方案/备选/理由/代价与实际验证 |
| [docs/project-plan.md](../docs/project-plan.md) | 实际成果、分支状态与下一步 |
| [docs/worktree-collaboration-plan.md](../docs/worktree-collaboration-plan.md) | 三个独立分支允许文件/提交/整合记录 |
| [docs/stage6-acceptance.md](../docs/stage6-acceptance.md) | 021/独立项回帖真实验收范围 |
