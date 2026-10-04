# IM @AI Outbox 基础审查（2026-10-04）

业务代码 bd945fe，整合分支 codex/agent-trigger-outbox-integration；共同 a303006、事务6c01a46、发布38c2e2f、接入4bf8a33，root无冲突整合并补流程测试。main仍f7abfd5，未push或云同步。三个原worktree干净保留。方案A57—A61均由用户明确选择；本轮只完成IM侧可靠通知，不标群内@AI完整功能或阶段6完成。

## 九步结果

| 步骤 | 实际目的与改动 | 验证 |
| --- | --- | --- |
| 1 共同 | 严格命令解析、不可变触发事实、最小事件、仓库接口、配置及022/init，固定协作契约和五项选择 | model命令过滤、字符串ID/事件、初始化与迁移一致性通过 |
| 2 事务保存 | 嵌入旧仓库，仅启用时换Create；原消息/Outbox一事务，重读数据库秒精度时间；失败回滚 | 默认GORM只有一个外层事务，提交/回滚/普通消息与普通群通过 |
| 3 重复与存储 | 原消息ID/原时间复用，1062完整事实比较；pending有界排序，锁内比对后发布标记 | 意图各字段冲突、坏记录/NULL/数量/顺序、标记重放及提交失败通过 |
| 4 发布 | 独立主题同步RequireAll；每轮≤100、轮次10秒/写5秒，不持SQL锁跨Kafka；ACK后标记 | 读/写/保存失败、部分成功、同key和字节重发通过 |
| 5 后台循环 | 约1秒可取消轮询，阶段错误不含私密正文；不替调用者Close | 分阶段取消、自有超时、失败循环与安全日志通过 |
| 6 配置接入 | 开关默认false，关闭仍旧仓库、不建新store/writer；启用早期拒绝空broker或混用聊天主题 | 两态factory及生产组件接线、主题空白/相等边界通过 |
| 7 生命周期 | 共享ctx启动消费者/发布器；退出cancel、等待两循环，然后Close两资源 | 阻塞runner、关闭次序及双错误保留通过 |
| 8 root组合 | 真实Pusher→GORM事务仓库→在线HTTP投递→真实publisher/store，加SQL/Kafka替身 | Outbox失败无投递；Kafka失败不阻止已保存聊天，后续同事件重发通过 |
| 9 审查 | 根agent集中检查、提交/无冲突整合、全部Go回归与Linux Push编译，整理全部文件与未验收项 | 全部通过；最终仅补文档，无新业务目标 |

第一次组合测试失败是root的sqlmock参数顺序与GORM实际ID列位置不同；只修fixture，定向再测及最终全量通过，生产代码未因此修改。root规范Go换行/格式后Git确认没有额外语义修改。

## 调用链和可靠性边界

启用时，WS原有鉴权/Kafka聊天链→Push Pusher.HandleMessage→新MessageRepository.Create→同MySQL事务保存/复用原消息→按ID重读权威内容与时间→IM groups读取团队→Outbox唯一记录→提交→原在线/离线聊天投递。只有用户团队群文本的首部 `@AI 整理任务 <1—2000字指令>` 触发；私聊/机器人/普通正文/引用/旧群不触发；无效命令仍普通聊天。

另一循环ListPending→最小JSON version/action/message_id（字符串）与固定agent-trigger:tasks:消息ID key→Kafka同步ACK→短SQL事务FOR UPDATE比较全意图→published=1。失败保存pending，进程重启可从数据库再读；允许重复事件。Published只意味着Kafka确认，不意味着Agent消费、草稿完成或消息送达。Outbox发布可以继续重试，与A61最多两次模型生成不同。

原作者/团队群/指令/参考时间留在IM表，无用户登录Token入库或入Kafka。参考时间来自已入库消息，不能用每次重试时刻。本批不跨库读User团队、不根据Kafka自报actor授予权限；后续A60必须通过专用mTLS受限RPC检查触发者当前资格，A61再实现Agent状态/租约/最多两次模型尝试，固定请求键去重。Task和卡片仍本人逐项确认/原有显式重试。

## 实际验证与未验证

root先在事务、发布各自稳定工作树集中执行包测试；整合四包首次仅rootfixture失败，修正后的流程定向通过。最终 `go test ./... -count=1` 全量通过，新增30个测试函数（含参数化子项）；`GOOS=linux GOARCH=amd64 go build ./cmd/push` 输出到临时目录通过。原前端脚本未变，本轮未重复已通过的187项Node验证，也未重复构建未改的IM/Agent/Gateway。子agent只编辑/gofmt/diffcheck，没有审批等待或自行测试/提交/合main。

MySQL/GORM使用SQL替身，Kafka writer使用替身，组合在线投递为实际本机httptest。没有执行真实MySQL/022迁移/Kafka/跨进程崩溃恢复、多发布者并发、进程信号、模型、浏览器、证书/容器/云部署。默认开关false；必须先执行022并准备后续消费者、受限身份/权限链和对应验收后才启用。发布失败或坏待发记录会留待重试，首条持续失败可能阻塞本批后续，当前不新增死信队列或后台模型权限。

事务agent约6分35秒、发布约9分20秒、接入约7分28秒，并行交付；没有单agent对照，不声称固定倍数提速。

## 全部22个实际修改文件（相对main f7abfd5）

| 文件 | 作用 |
| --- | --- |
| [agent_trigger.go](D:/zy/GoLang/go-im/internal/model/agent_trigger.go) | 命令、事实与通知形状 |
| [agent_trigger_test.go](D:/zy/GoLang/go-im/internal/model/agent_trigger_test.go) | 过滤、稳定通知与schema检查 |
| [agent_trigger_contract.go](D:/zy/GoLang/go-im/internal/repository/agent_trigger_contract.go) | 窄store接口 |
| [agent_trigger_repo.go](D:/zy/GoLang/go-im/internal/repository/agent_trigger_repo.go) | 原子保存及完整意图发布标记 |
| [agent_trigger_repo_test.go](D:/zy/GoLang/go-im/internal/repository/agent_trigger_repo_test.go) | GORM/SQL替身测试 |
| [agent_trigger_publisher.go](D:/zy/GoLang/go-im/internal/push/agent_trigger_publisher.go) | 同步Kafka通知与可取消循环 |
| [agent_trigger_publisher_test.go](D:/zy/GoLang/go-im/internal/push/agent_trigger_publisher_test.go) | 确认、超时、重发及循环 |
| [agent_trigger_flow_test.go](D:/zy/GoLang/go-im/internal/push/agent_trigger_flow_test.go) | root完整消息/事件流程 |
| [main.go](D:/zy/GoLang/go-im/cmd/push/main.go) | Push进程启动/退出 |
| [agent_trigger.go](D:/zy/GoLang/go-im/cmd/push/agent_trigger.go) | 配置及有限接入helper |
| [agent_trigger_test.go](D:/zy/GoLang/go-im/cmd/push/agent_trigger_test.go) | 两态与退出次序 |
| [config.go](D:/zy/GoLang/go-im/internal/config/config.go) | Kafka两个新配置字段 |
| [go-im.yaml](D:/zy/GoLang/go-im/config/go-im.yaml) | 本地默认关闭/独立主题 |
| [docker-config.yaml](D:/zy/GoLang/go-im/deploy/docker-config.yaml) | 容器样例默认关闭/独立主题 |
| [init.sql](D:/zy/GoLang/go-im/deploy/mysql/init.sql) | 新库Outbox表 |
| [022_im_agent_trigger_outbox.sql](D:/zy/GoLang/go-im/deploy/mysql/migrations/022_im_agent_trigger_outbox.sql) | 已有库待执行增量 |
| [agent-trigger-outbox-contract.md](D:/zy/GoLang/go-im/docs/agent-trigger-outbox-contract.md) | 共同API/范围及分工 |
| [agent-mention-design.md](D:/zy/GoLang/go-im/docs/agent-mention-design.md) | 提案、用户确认及本轮边界 |
| [architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A57—A61确认、备选/理由/代价 |
| [project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 当前成果与下批方向 |
| [worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三worktree/允许范围及交付 |
| [agent-trigger-outbox-review.md](D:/zy/GoLang/go-im/docs/agent-trigger-outbox-review.md) | 本审查记录 |
