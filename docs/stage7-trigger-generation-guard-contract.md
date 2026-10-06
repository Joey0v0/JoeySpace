# 阶段7：后台Agent触发上下文代际核权

2026-10-06，起点25de3fc，集成codex/stage7-trigger-generation-guard。沿用户确认A75的User成员资格版本与IM持久关闭记录，把后台Agent的触发上下文读取接入同一代际规则。本批不开放退出/清理RPC，不改变mTLS身份、Kafka事件格式、触发保存范围、普通JWT入口、Push或页面。最多六步：共同协议/契约、User响应、IM客户端、IM触发上下文、整合验证、记录。

## 固定协议和处理顺序

- UserTrigger.CheckTriggerTeamMemberResponse新增field3 `generation`（int64），保留actor/team原字段与方法。旧调用端忽略新增字段；新IM必须要求正generation，拒绝旧User的0响应，部署User先升级。User只为活动成员且启用账户返回其DB当前版本，不从RPC入参自报版本或JWT提取，不回资料/角色。
- IM的triggerTeamClient保留`Check(ctx,actor,team) error`用于旧调用方和现有测试，新增`CheckGeneration(ctx,actor,team)(int64,error)`。二者都走原专用mTLS、只派生持久trigger的actor/team、不转发用户Token；Check复用CheckGeneration，nil/范围不符/版本<=0固定Unavailable。错误码映射沿旧逻辑。
- root在triggerTeamEligibility增加CheckGeneration方法，执行Agent不改该共同接口。测试替身可为旧有成功路径返回固定正1，生产禁止默认1。User返回大版本如9007199254740993须精确保留。
- ReadTaskTriggerContext仍先从IM持久Outbox/原消息派生固定范围并查一次当前群资格，再CheckGeneration；随后使用同一条IM新读SQL检查当前group_members/群team/closedThrough<generation，才读取历史。历史形成后再次CheckGeneration，要求与前一次完全相同，随后再次检查IM当前群成员/关闭边界才返回；版本改变或已关闭拒绝，不返回部分历史。SQL和User服务之间不持锁，跨服务撤权仍非瞬时原子。
- ResolveTaskTriggerMember通过ReadTaskTriggerContext获得受保护的来源；其后候选解析和末次群检查本批保持既有行为，尚未把末次校验纳入代际一致性。下一批收口这一单独路径和User Resolver前后版本比较，不把本批说成后台所有读取完成。
- SQL/User和Kafka仍替身；真实MySQL/031/032/mTLS部署证书/浏览器/模型和云仍最终验收。无新迁移、端口或密钥。

## 三个执行任务

| 角色 | 绝对工作目录 | 分支 | 唯一允许文件 |
| --- | --- | --- | --- |
| A User版本响应 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-user-trigger-generation | rpc/user/trigger_team.go、trigger_team_test.go、trigger_member_test.go、trigger_member_flow_test.go、trigger_tls_flow_test.go、trigger_membership_active_test.go；可新增rpc/user/trigger_generation_test.go |
| B IM专用客户端 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-trigger-client-generation | rpc/im/trigger_team_client.go、trigger_team_client_test.go；可新增rpc/im/trigger_client_generation_test.go |
| C IM触发上下文 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-trigger-context-generation | rpc/im/trigger_context.go、trigger_context_test.go、trigger_context_flow_test.go、trigger_compose_test.go、trigger_listener_test.go、trigger_assignee_test.go、trigger_assignee_flow_test.go；可新增rpc/im/trigger_context_generation_test.go |

root拥有trigger.proto/生成、trigger_context_contract.go、本契约、Git/集中测试/文档。执行Agent仅编辑/gofmt/diffcheck，不test/build/Git写/mainmerge/push/部署；不越界到普通IM/Agent/User其他方法/生成文件。若发现契约或权限模型冲突，暂停受影响改动报告root。三个worktree从共同提交建干净独立分支。其余未碰文件不因允许范围而硬改。

## 本批实现与审查

共同cef453d；root审查并保存B 595f083、A f85434e、C dcd93f7，三个独立分支无冲突合入本轮集成分支。三个worktree保持干净，main仍89e2a1e；未合main、push、执行迁移或部署。用户已确认的A75规则没有改选型，不新增服务、表、中间件、依赖或端口。

| 小步 | 做什么、为何这样做及结果 |
| --- | --- |
| 1 共同协议 | UserTrigger响应新增field3 `generation`，仅由User现有成员表真实值生成；IM共同接口新增`CheckGeneration`，保留旧`Check`方法，以免各执行分支自行更改协议。重新生成`trigger.pb.go`，未删除`trigger_grpc.pb.go` |
| 2 User资格 | 在现有专用mTLS身份和active成员查询中读取`users.status`与`team_members.generation`；仅启用账户、正版本返回固定actor/team/version。坏版本、列缺失、重复行或数据库错误拒绝且不暴露原始错误。测试把身份和大版本分别构造，防止错读为同一列 |
| 3 IM客户端 | 专用IM→User mTLS客户端严格要求非空响应、原actor/team及正版本；`Check`复用同一校验，旧User成功响应缺field3固定失败。大于JavaScript安全整数的int64经gRPC仍精确，不转发用户Token，不新增重试 |
| 4 触发上下文 | IM先从持久Outbox/原消息取得授权范围，再查现有群资格。User正版本→IM当前成员/群team/关闭边界→读并核实历史→User再次返回相同版本→IM再次核对群/关闭边界→返回；失败无部分正文。负责人解析调用该上下文因此继承来源保护，但候选解析后的末次检查暂沿旧逻辑 |
| 5 集中验证 | root执行`go test ./rpc/user ./rpc/im -count=1 -timeout=90s`与`go test ./... -count=1 -timeout=90s`均通过；本机专用双mTLS测试含旧User零版本、读中换代/撤权/关闭与精确大版本。三个执行Agent只做gofmt/diffcheck，未自行测试或Git写；页面未改，不重复Node测试 |
| 6 边界记录 | 更新计划/ADR/部署/阶段7验收与协作记录，明确部署顺序及剩余候选解析、User解析器/退出/Push缺口，不把局部保护标为阶段7完成 |

主要调用链：后台Agent用专用mTLS向IM读取已保存的触发消息→IM以持久范围向User专用mTLS资格RPC取得真实正generation→IM SQL核对群成员和关闭代际→查询历史→再次向User/IM核验同一代际→才返回完整上下文。检查是顺序快照，不持有跨RPC数据库锁；不能保证在最后检查与网络返回之间实现瞬时原子撤权。旧User返回零版本需先升级User及031；IM依赖032的关闭表，按部署文档再升级IM。

真实MySQL查询/索引/并发、031/032迁移、真实证书和Compose/浏览器/云/模型均未验证。下一批仍须补`ResolveTaskTriggerMember`候选解析后的最终User版本及IM关闭核对、User解析器查询前后版本比较；之后才是退出/清理/重入和Push资格链。相对25de3fc的全部实际修改共23份：

| 文件定位 | 本批作用 |
| --- | --- |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md) | User/IM及031/032升级顺序和局部边界 |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A75本批选择、备选与代价 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 当前进度与下一步 |
| [docs/stage7-acceptance.md](D:/zy/GoLang/go-im/docs/stage7-acceptance.md) | 本地证据和真实环境缺口 |
| [docs/stage7-trigger-generation-guard-contract.md](D:/zy/GoLang/go-im/docs/stage7-trigger-generation-guard-contract.md) | 共同接口、分工及审查结果 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三个分支与root实际工作 |
| [rpc/im/trigger_assignee_flow_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_assignee_flow_test.go) | 负责人解析间接上下文实际mTLS测试预期 |
| [rpc/im/trigger_assignee_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_assignee_test.go) | 负责人解析替身与复核次数预期 |
| [rpc/im/trigger_context.go](D:/zy/GoLang/go-im/rpc/im/trigger_context.go) | 历史前后User版本/IM当前群关闭核验 |
| [rpc/im/trigger_context_contract.go](D:/zy/GoLang/go-im/rpc/im/trigger_context_contract.go) | 新资格版本接口 |
| [rpc/im/trigger_context_flow_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_context_flow_test.go) | 生产mTLS触发上下文场景 |
| [rpc/im/trigger_context_generation_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_context_generation_test.go) | 版本变动、拒绝、坏存储和边界测试 |
| [rpc/im/trigger_context_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_context_test.go) | 历史查询与二次核验的SQL预期 |
| [rpc/im/trigger_listener_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_listener_test.go) | 运行配置替身接口适配 |
| [rpc/im/trigger_team_client.go](D:/zy/GoLang/go-im/rpc/im/trigger_team_client.go) | 生产专用User客户端回显/版本校验 |
| [rpc/im/trigger_team_client_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_team_client_test.go) | 零/负/范围错误与大版本mTLS测试 |
| [rpc/user/pb/trigger.pb.go](D:/zy/GoLang/go-im/rpc/user/pb/trigger.pb.go) | Protobuf响应field3生成代码 |
| [rpc/user/trigger.proto](D:/zy/GoLang/go-im/rpc/user/trigger.proto) | UserTrigger协议field3 |
| [rpc/user/trigger_member_flow_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_member_flow_test.go) | User候选解析既有资格查询列适配 |
| [rpc/user/trigger_member_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_member_test.go) | User候选解析既有资格查询列适配 |
| [rpc/user/trigger_team.go](D:/zy/GoLang/go-im/rpc/user/trigger_team.go) | 真实活动成员generation回显 |
| [rpc/user/trigger_team_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_team_test.go) | 存储异常、版本与保密错误测试 |
| [rpc/user/trigger_tls_flow_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_tls_flow_test.go) | User生产mTLS大版本/无效版本回显测试 |
