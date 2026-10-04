# Agent 持久通知接收：九步审查

日期：2026-10-04。本轮沿A59/A61及用户本轮明确选择的A62。上批71d3161已快进合本地main；主目录D:/zy/GoLang/go-im，本轮分支codex/trigger-inbox-integration，共同d6fb988。保留三个worktree，不删除旧文件，不push/部署。

## 九步实际范围

| 步骤 | 内容和解决的问题 | 关键边界 |
| --- | --- | --- |
| 1 | root统一接收契约及023/init队列表 | Agent拥有最小通知；queued表示排队，未授权读取、未生成草稿 |
| 2 | 存储事务接收来源ID/action/version | INSERT成功提交才能确认，不持久化Token、正文或自报actor |
| 3 | 重复通知锁读不可变事实及失败测试 | 相同来源只一行，不更新接收时间；坏保存数据拒绝，SQL失败可重试 |
| 4 | 严格JSON三字段、规范大ID及Key校验 | 未知/重复/缺字段、非法UTF8/超限、版本和Key错误拒绝；Header不是身份 |
| 5 | 串行消费→存储→同步确认 | 前条未完成不确认更高offset；Commit失败只重试确认，不重新接收；A62异常不确认 |
| 6 | 默认关闭的配置和Agent进程接线 | 专用主题/消费组、FirstOffset、同步commit，无模型或权限旁路 |
| 7 | 可取消生命周期及普通RPC独立性验证 | 取消→等Run→关闭Reader，再释放clients/DB；异常停consumer不停止本人RPC |
| 8 | root实际publisher→consumer→GORM的替身组合 | 真生产JSON/Key兼容；SQL/ACK不确定及重启重复仍同一来源；023与初始化定义一致 |
| 9 | root集中Go/LINUX验证和文档记录 | 子agent不自行tests/build/Git；只把实际验证结果标完成 |

## 调用链与确认含义

IM消息与Outbox事务 → 现有发布器同步Kafka ACK → Agent FetchMessage（不提前确认）→ 严格解析 → Agent Inbox事务保存queued → 同步CommitMessages。

本批消费者不读取群正文，也不调用IM/User/模型/Task；后续租约执行先用上批Agent→IM受限接口核对当前资格和原消息参考时间，再生成本人审查草稿。queued只是接收成功，不能表示正在生成或已有任务。

A62异常会停止本进程的触发消费，原offset仍未确认；普通重启不会自动跳过错误通知，需要修正版本/配置或人工处理故障。临时SQL/Fetch/Commit错误保持同条并可取消重试；这些不消耗模型两次预算。首次专用新组从FirstOffset读已保留通知，不保证超过Kafka保留期的记录能恢复，本批不回扫已published Outbox。

## 验证结果与限制

业务整合9abf15f：`go test ./... -count=1` 一次全部通过；Linux amd64 `./cmd/agent` 编译通过，产物仅临时目录。新增28个测试函数及参数场景：store6、decoder3、consumer6、runtime9、root组合4。没有改变IM/User/Task的生产代码、协议或前端，不重复运行未变前端测试/其他Linux构建。

组合使用现有真实发布器生成的JSON和Key、新真实decoder/consumer/store及GORM，Kafka为接口替身、MySQL为SQL替身；SQL保存失败后同条重试、Commit失败不重新保存、不确定SQL提交必须核对同源重复后才ACK、重启重放与坏保存事实不ACK均通过。023和初始化定义一致检查不等同已执行迁移或真实并发验收。

runtime测试另使用实际本机TCP及生产Agent.Ask，坏通知停止后本人RPC仍可用，未确认通知；SQL接收commit顺序、取消等待Run后才Close、并发Stop/Close一次、安全记录停止原因不自动重启均通过。没有运行真实Kafka消费组再平衡、偏移持久化、真实Linux信号或全进程启动验收。测试使用替身answerer，不访问真实模型。

023尚未执行，消费者默认关闭，IM Outbox开关仍false。没有启动真实Kafka/MySQL/模型/浏览器/容器/腾讯云。没有租约、模型计数/自动恢复或后台草稿页面；下一批继续A61持久执行，后台负责人解析也须按A60设计受限通道，不伪造用户Token。既有本人确认Task及显式回帖重试不变。

## 全部实际修改文件（18个）

生产已有文件修改集中在Agent启动和初始化SQL，其余主要为新增接收模块、测试及记录。审查位置如下。

| 文件 | 用途 |
| --- | --- |
| [trigger_inbox_contract.go](D:/zy/GoLang/go-im/rpc/agent/trigger_inbox_contract.go) | 共享接口/事件校验/异常哨兵 |
| [023_agent_trigger_inbox.sql](D:/zy/GoLang/go-im/deploy/mysql/migrations/023_agent_trigger_inbox.sql) | 未执行的增量队列表迁移 |
| [init.sql](D:/zy/GoLang/go-im/deploy/mysql/init.sql) | 新库相同队列表 |
| [trigger_inbox_store.go](D:/zy/GoLang/go-im/rpc/agent/trigger_inbox_store.go) | 事务持久接收及不可变去重 |
| [trigger_inbox_store_test.go](D:/zy/GoLang/go-im/rpc/agent/trigger_inbox_store_test.go) | SQL失败/重放/坏事实/取消验证 |
| [trigger_notification.go](D:/zy/GoLang/go-im/rpc/agent/trigger_notification.go) | 严格JSON/大ID/Key解析 |
| [trigger_notification_test.go](D:/zy/GoLang/go-im/rpc/agent/trigger_notification_test.go) | 格式/版本/UTF8/边界验证 |
| [trigger_consumer.go](D:/zy/GoLang/go-im/rpc/agent/trigger_consumer.go) | 顺序接收确认及A62停止策略 |
| [trigger_consumer_test.go](D:/zy/GoLang/go-im/rpc/agent/trigger_consumer_test.go) | offset顺序/失败重试/取消/资源验证 |
| [main.go](D:/zy/GoLang/go-im/cmd/agent/main.go) | 配置/后台接收/退出接进程 |
| [trigger_inbox.go](D:/zy/GoLang/go-im/cmd/agent/trigger_inbox.go) | 配置、reader工厂、安全故障报告和runtime |
| [trigger_inbox_test.go](D:/zy/GoLang/go-im/cmd/agent/trigger_inbox_test.go) | 禁用/配置/真实普通RPC独立性与退出验证 |
| [trigger_inbox_flow_test.go](D:/zy/GoLang/go-im/rpc/agent/trigger_inbox_flow_test.go) | root生产发布消费+SQL替身组合/schema检查 |
| [trigger-inbox-contract.md](D:/zy/GoLang/go-im/docs/trigger-inbox-contract.md) | 共同契约和分工 |
| [trigger-inbox-review.md](D:/zy/GoLang/go-im/docs/trigger-inbox-review.md) | 本审查记录 |
| [architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A62选项/理由/代价/用户确认，A61实施分解 |
| [project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 实际进展和下一步 |
| [worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 分支/文件限制/交付整合 |

交付共同d6fb988、存储3391102、消费5d05ab2、进程6b46654；root按存储→消费→进程无冲突整合，再补真实producer+consumer+SQL替身组合为业务9abf15f。最终全量Go/Linux Agent通过，无测试修复或生产返工。main保持上批71d3161，本轮分支codex/trigger-inbox-integration供审查，三个worktree干净保留；未push/云同步。

三角色自行记录约4分43秒、9分43秒、9分35秒，只有并行交付记录，不声称固定倍数速度。下一批继续A61持久租约、执行与模型两次预算；后台负责人解析/本人草稿入口仍分别补齐，阶段6保持未全部完成。
