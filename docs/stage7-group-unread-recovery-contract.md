# 阶段7：群历史重新遍历与未读恢复

日期：2026-10-06。共同起点：73ade5b；本批集成分支：codex/stage7-group-unread-recovery。沿已确认A73，不新增协议、表、依赖或权限规则。

## 本轮接口与边界

1. 页面新增“从最新消息重新遍历”按钮，调用 `doRestartTeamGroupHistory()`；内部调用 `loadTeamGroupHistory(true, true)`。原调用及默认参数保持兼容。
2. 第二参数 `restart = false` 仅用于显式重新遍历。重遍历忽略旧结束状态和旧游标，读取最新20条；只有当前身份/范围的有效成功响应才替换分页游标与结束状态。失败、非法响应、过期响应不改变原分页位置。
3. “刷新最新群消息”仍只刷新最新页，不改变旧分页位置。三种操作共享已有在途请求保护、身份/范围世代检查及msg_id去重。成功页交给现有未读控件；GET与重新遍历均不自动确认已读。
4. 迟到的小ID消息可在重新遍历后逐页找到。重遍历不是数据库快照，也不保证持续新增消息期间一次遍历涵盖所有消息；未读数和逐消息凭据仍由IM当前资格检查决定。
5. 提交后丢响应可已有已读凭据。验证先查询再由本人显式重试相同ID，不自动换页或扩大确认范围；重试的SQL不修改首次read_at。测试拦截器只用于模拟响应失败，不进入生产代码。

## 三个执行任务

| Agent任务 | 绝对工作目录 | 分支 | 唯一允许编辑文件 |
| --- | --- | --- | --- |
| A 页面分页 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-history-restart-page | examples/chat.html、examples/chat.test.cjs |
| B 页面恢复组合 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-unread-page-recovery-test | 新examples/team-group-unread-recovery.test.cjs |
| C IM恢复组合 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-unread-rpc-recovery-test | 新rpc/im/team_group_unread_recovery_test.go |

root统一共同文档、审查、测试、提交及集成，不合main、不push或部署。执行Agent只编辑允许文件和格式/差异检查，不运行test/build或Git写操作；必要时root将已审查的页面提交提供给B。整批最多九步：共同边界、A、B、C、审查整合、集中验证、记录，不凑满九步。

## 验证与限制

页面组合加载实际HTML内联代码与实际未读脚本，HTTP/DOM为替身；IM组合使用生产处理器、本机TCP gRPC，SQL和User资格为替身。真实MySQL首次时间/重启、030迁移、浏览器、Kafka/Compose/云端及真实模型仍留最终统一验收，不能据此标记阶段7或整个项目完成。

## 本批实现与审查

实际共同eff7252；A96f81d8、Ba8daf70、Cfff1083均由root审查、定向测试后保存。root先将A快进提供B，使B测试实际新增入口，再快进整合A/B并无冲突合入C（82ee844）。子Agent没有自行测试/build、提交、合main或部署；三个worktree干净保留，root文档与最终审查提交在本轮集成分支。main仍89e2a1e，未合main/push/执行迁移或部署。

| 小步 | 实际改动、目的及验证 |
| --- | --- |
| 1 共同边界 | 单独固定重遍历动作、失败不改位置、恢复测试窗口与三个目录/文件；不增加协议或表 |
| 2 页面分页 | 一个独立按钮、一个包装函数及原历史函数的restart参数；仅有效当前成功时替换位置，保留latest语义，165项对应页面测试通过 |
| 3 页面恢复组合 | 5个主test含20个叶场景，Node计24项；实际执行HTML内联+未读模块，低ID迟到后重新遍历找到、只确认本页、结果不确定显式同ID重试、身份往返/JSON等待后旧回包隔离均通过 |
| 4 IM恢复组合 | 2项本机实际TCP/生产处理器测试；成功提交后拦截器改Unavailable、提交后User拒绝再恢复，原Token核权/唯一ID集合/查询只读/同ID冲突no-op通过 |
| 5 审查整合 | 只改4份实现/测试文件，唯一生产变更是chat.html；root修一项新测试的固定微任务等待，改等实际json()进入信号，生产逻辑未因此改动；三分支无冲突整合 |
| 6 集中验证 | `node --test examples/*.test.cjs`：353项通过；`go test ./... -count=1 -timeout=90s`：全仓通过；GOCACHE在TEMP、GOFLAGS=-p=1。不在通过后无故重复扩大验证 |
| 7 记录 | 更新选择/备选/代价、计划、部署说明、真实验收窗口及本表；未完成的A16/日志/真实环境仍明确保留 |

调用链：按钮→同一个历史读取函数→Gateway历史GET→现有IM消息ID分页；有效页把具体ID交未读控件，只有本人点击确认才POST→Gateway→IM事务写个人凭据并复核权限。重新遍历既不写已读也不调用离线ACK。前端组合的HTTP是替身，IM组合另走真实本机TCP，但两段不合称完整HTTP→真实MySQL链。

恢复证据：第一次Mark的生产处理器已完成事务提交/后续核权及COUNT，测试拦截器才替换成功回包；之后本人GET、同集合Mark重试返回相同范围和未读数。另一次提交后User核权拒绝，未返回成功数据；当前资格恢复后同ID可重试。严格SQL不包含read_at插入/更新或ID高水位，重试影响行数0由SQL替身给定，因此不声称实际数据库首次时间或真实离队入口已验收。

本批相对73ade5b的全部实际修改文件共10份：

| 文件定位 | 实际内容 |
| --- | --- |
| [examples/chat.html](D:/zy/GoLang/go-im/examples/chat.html:155) | 独立按钮/提示、历史函数restart参数和包装动作（函数在1992/2071行） |
| [examples/chat.test.cjs](D:/zy/GoLang/go-im/examples/chat.test.cjs:934) | 扩展旧范围/在途保护场景，新增结束恢复、失败保位置、JSON边界和按钮检查 |
| [examples/team-group-unread-recovery.test.cjs](D:/zy/GoLang/go-im/examples/team-group-unread-recovery.test.cjs:116) | 真实页面内联/未读模块恢复组合，DOM/HTTP替身 |
| [rpc/im/team_group_unread_recovery_test.go](D:/zy/GoLang/go-im/rpc/im/team_group_unread_recovery_test.go:133) | 生产IM TCP提交后失败及撤权恢复，SQL/User替身 |
| [docs/stage7-group-unread-recovery-contract.md](D:/zy/GoLang/go-im/docs/stage7-group-unread-recovery-contract.md:1) | 共同边界、精确任务、七步结果及全部文件定位 |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md:414) | A73内重遍历/故障注入方案、备选、代价和确认状态 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md:7) | 本批成果、验证限制和下一步A16讨论；整体仍未完成 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md:504) | 本轮共同起点、三个允许文件范围及集成事实 |
| [docs/stage7-acceptance.md](D:/zy/GoLang/go-im/docs/stage7-acceptance.md:62) | 本地组合证据、真实未读/迟到/故障/重启/资格恢复待验收项 |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md:3) | 030前提、两个历史动作差异、本人显式确认与不确定结果恢复使用说明 |

没有新增协议/生成代码/迁移/依赖、自动已读或后台重试。本轮完成的是A73已确认范围的本地恢复接线和验证，阶段7、A16退出清理、原路线单聊未读、安全关联日志和真实核心演示不标完成。
