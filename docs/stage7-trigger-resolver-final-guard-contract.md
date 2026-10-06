# 阶段7：后台负责人解析末次资格保护

2026-10-06，从a88ba53建立本轮集成分支。沿用户已确认的A75成员资格版本与IM永久关闭记录，本批只收口User候选解析和IM返回候选前的末次核权。最多六步：共同契约、User解析前后版本、IM解析后User/群复核、独立组合测试、集中验证、记录。无新协议、迁移、依赖、端口或权限模型。

## 固定调用与失败规则

- User ResolveTriggerTeamMember保留原专用mTLS身份、请求/响应、字面姓名匹配、候选限制和原查询。查询前后调用现有CheckTriggerTeamMember，取两次真实正generation；任何一次拒绝或版本不等均不返回候选（不把离队重入误判为连续授权）。候选目标仍限当前active/启用成员；User不额外开放资料。
- IM ResolveTaskTriggerMember先由ReadTaskTriggerContext取得已保存触发范围和受保护的历史，核对姓名确实出现在授权文本后，以持久来源的actor/team向User取正generation作为候选解析基线；然后调用User候选解析。成功结果经原严格回显/候选校验后，再向User取当前正generation，要求与基线相同，接着在IM检查当前群成员、team归属和032关闭边界；任一失败无部分候选。最终检查不用请求metadata、候选响应自报范围或旧群成员快照。不持跨RPC SQL锁，不能宣称瞬时原子撤权。本批为负责人解析额外增加两次User资格RPC和一次IM群关闭查询；复用既有ReadTaskTriggerContext的两次资格核对。
- User/IM现有错误码和安全错误正文沿用；版本不一致为PermissionDenied，零版本和坏存储为Unavailable。请求取消/超时优先按现有规则处理。旧User缺generation直接拒绝；部署仍先核对001/执行031升级User，再执行032升级IM。
- 本批不改ReadTaskTriggerContext已完成的前后核权、普通JWT入口、Agent生成、Push或退出/恢复/清理。真实MySQL、并发、迁移、浏览器和云仍留最终验收。

## 三个独立任务

| 角色 | 绝对工作目录 | 分支 | 唯一允许文件 |
| --- | --- | --- | --- |
| A User查询版本 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-user-resolver-generation | rpc/user/trigger_member.go、trigger_member_test.go、trigger_member_flow_test.go；可新增rpc/user/trigger_member_generation_test.go |
| B IM末次核权 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-im-resolver-final-fence | rpc/im/trigger_assignee.go、trigger_assignee_test.go、trigger_assignee_flow_test.go；可新增rpc/im/trigger_assignee_generation_test.go |
| C 独立组合测试 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-resolver-generation-flow | 仅可新增rpc/im/trigger_resolver_generation_flow_test.go |

root负责本契约、计划/ADR/部署/验收/协作文档、Git保存/整合及所有Go测试。三个worktree从同一共同提交建立干净分支；执行Agent仅编辑允许文件、gofmt/diffcheck，不test/build/Git写或自行合main/push/部署。若测试需要越界修改或改变契约，先向root报告。C的测试可以依赖B约定的最终User CheckGeneration→IM群关闭查询顺序，但不改B文件。未触及允许文件不硬改。

## 本批实现与审查

共同05da516；root审查并保存User e58366c、IM 37836e5、独立组合1ea9101，三个分支无冲突合入本轮集成分支。C首次组合测试暴露“只在解析后取当前有效版本会让查询中换代放行”的缺口；B因此补在候选调用前取得基线并在候选后比较同一版本，C同步核实四次User检查的调用顺序。这个修正沿既定A75，不新增方案或协议。三个worktree干净保留，main仍89e2a1e；未合main、push、执行迁移或部署。

| 小步 | 做什么、目的与实际结果 |
| --- | --- |
| 1 共同契约 | 固定User候选前后版本与IM候选前后版本/最终群关闭核验的顺序、失败规则、两个生产文件边界和独立组合测试；继续复用现有专用mTLS与正generation |
| 2 User查询 | ResolveTriggerTeamMember保存查询前真实版本，候选行读完后再查一次；两次版本不一致按PermissionDenied且不返回候选。零版本/坏数据仍Unavailable，原字面匹配SQL、20项/截断和超时不变；空结果也复核 |
| 3 IM返回 | ReadTaskTriggerContext已完成来源和历史核权；有文本姓名证据后，IM从持久范围取解析基线，再调User候选。严格验证候选后重新取User版本，要求同代际，再用该版本核对当前群成员、群team与关闭边界；任一失败无部分候选 |
| 4 独立组合 | 新增实际本机Agent→IM与IM→User双mTLS组合测试，覆盖有权成功、候选后撤权/换代/User故障、IM关闭/群移除、持久范围和不转发伪造Token；不同于仅模拟方法调用的单元测试 |
| 5 集中验证 | root执行`go test ./rpc/user ./rpc/im -count=1 -timeout=90s`及`go test ./... -count=1 -timeout=90s`均通过；`git diff --check`通过。执行Agent只做gofmt/diffcheck，未自行test/build/Git写；页面未改，不重复Node测试 |
| 6 边界记录 | 更新计划、ADR、部署、验收与协作记录；A75读取链的本地实现不等于已完成团队退出、IM受控清理、Push资格或真实部署 |

关键调用链：Agent专用mTLS→IM持久触发范围及受保护历史→IM向User取候选基线generation→User专用mTLS候选查询前后检查同一generation→IM再向User取相同generation→IM一条SQL核对当前群成员、群team和closed_through_generation→仅成功才返回候选。User服务不会把候选版本直接给IM，IM用自己的前后资格检查覆盖User响应后的窗口。检查跨User和IM，没有跨服务事务或瞬时原子撤权保证。

真实MySQL语法、031/032迁移、并发锁、实际证书/Compose/云、浏览器及模型均未验收。下一步是沿A75/A77实现User持久退出、受控IM清理/显式恢复/完成后重入，之后沿A76实现Push专用核权；阶段7仍未完成。相对a88ba53全部实际修改共13份：

| 文件定位 | 本批作用 |
| --- | --- |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md) | User/IM升级顺序与本批局部边界 |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A75本批备选、选择理由与代价 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 当前进度、下一步和核对标记 |
| [docs/stage7-acceptance.md](D:/zy/GoLang/go-im/docs/stage7-acceptance.md) | 本地证据与实库/部署缺口 |
| [docs/stage7-trigger-resolver-final-guard-contract.md](D:/zy/GoLang/go-im/docs/stage7-trigger-resolver-final-guard-contract.md) | 共同契约、三任务与审查结果 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三分支及root实际集成过程 |
| [rpc/im/trigger_assignee.go](D:/zy/GoLang/go-im/rpc/im/trigger_assignee.go) | 解析前后User同代际、最后IM群关闭核验 |
| [rpc/im/trigger_assignee_flow_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_assignee_flow_test.go) | 既有双mTLS解析成功/失败顺序适配 |
| [rpc/im/trigger_assignee_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_assignee_test.go) | 基线失败/换代/关闭、无部分候选测试 |
| [rpc/im/trigger_resolver_generation_flow_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_resolver_generation_flow_test.go) | 独立双mTLS端到端资格变化组合 |
| [rpc/user/trigger_member.go](D:/zy/GoLang/go-im/rpc/user/trigger_member.go) | 候选查询前后generation相等约束 |
| [rpc/user/trigger_member_flow_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_member_flow_test.go) | User生产TLS换代拒绝场景 |
| [rpc/user/trigger_member_generation_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_member_generation_test.go) | 空/非空、大int64/逆向/非法版本测试 |
