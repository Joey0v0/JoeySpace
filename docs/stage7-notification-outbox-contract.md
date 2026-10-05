# 阶段7：Task通知事件存储与发布契约

2026-10-05，用户明确选择A69持久事件/Kafka/WS、A70专用mTLS。基线39c5493，main89e2a1e；本批codex/stage7-notification-outbox，不合main或部署。最多八步：共同契约1、事务写入1、待发布读取/确认1、发布器1、事件/迁移验证1、进程接线1、集中测试1、文档审查1。

## 存储与事件

Task新增task_notification_outbox表，notification_id复用通知行自增ID作为主键，team_id/recipient_id/event_version固定，不存Token或任务正文。published默认false，created_at数据库时间，published_at首次Kafka确认后更新；索引(published,notification_id)。029/init一致；依赖027/028，迁移不回填旧通知，不突然提醒历史记录。

SetTaskStatus真实变化时原事务依次写状态/操作，每个去重接收人写通知并取得自增ID，紧接着写对应Outbox。notification结构追加ID但保留INSERT四字段兼容；ID须正，通知或事件失败整笔回滚，禁止事务内网络发送。重复提交当前状态无新操作/通知/事件。Outbox写入一直启用，发布默认关闭仅停止发Kafka；升级Task前需029，即使发布关闭也不能省略迁移。

共享model.TaskNotificationEvent(version=1,type=task_notification_changed,notification_id/team_id/recipient_id均正int64且JSON字符串)。事件ID就是notification_id；Kafka key固定task-notification:<notification_id>，同事件重试payload/key不变，不承诺跨分区顺序。事件不是授权，后续WS仍查连接本人团队资格，页面通过Task读取正文，不自动标已读。

## Store与发布器固定接口

主agent的rpc/task/notification_outbox_contract.go定义taskNotificationOutboxRow及接口。执行A实现newTaskNotificationOutboxStore(db *gorm.DB) taskNotificationOutboxStore：ListPending(ctx,limit)只读未发、ID升序、有界1—100，校验所有行/顺序/重复；MarkPublished(ctx,row)仅原notification/team/recipient/version一致且未发时更新published=true和数据库published_at，重试已发且事实相同可成功，缺失或事实冲突失败，不覆盖首次时间。不得读User/IM表，不改通知已读状态。采用短SQL事务行锁保存发布确认，Kafka不在锁内。

执行B实现newTaskNotificationPublisher(store,writer,logger *zap.Logger) *taskNotificationPublisher，RunOnce(context.Context) error及Start(context.Context)。每轮最多100，总10秒，单Kafka5秒；预检整批合法、未发、升序及容量后顺序发送同步RequireAll/Async=false/MaxAttempts=1。仅WriteMessages成功且context有效后MarkPublished，确认不确定或标记失败保留原事件可重发；任一步失败本轮停止，下一轮间隔1秒避免忙循环。错误/日志用固定阶段文字，不打印SQL、地址、Token或事件详情。缺依赖安全失败，Start可取消并由进程等待后关闭writer。多发布进程允许重复，当前无租约、不声称只发送一次；后续消费者按固定ID合并提醒。

进程接线由root实现：TASK_NOTIFICATION_PUBLISH_ENABLED默认false，严格布尔；启用需TASK_NOTIFICATION_KAFKA_BROKERS逗号分隔合法host:port、TASK_NOTIFICATION_TOPIC合法Kafka名称，不能等于当前chat_messages/agent_task_triggers，并用TASK_NOTIFICATION_CHAT_TOPIC/TASK_NOTIFICATION_AGENT_TOPIC覆盖实际保留名以校验私有配置。默认不开Kafka客户端；启用时开生产GORMstore及kafka.Writer。RPC正常/信号停机先取消并等待发布器，再关闭writer/SQL；不以异步发布故障回滚已提交任务。配置合法性启动时检查，不接真实broker。

## 三worktree范围

| 执行者 | 绝对目录及新分支 | 唯一允许文件 |
| --- | --- | --- |
| A事务/存储 | D:/zy/GoLang/go-im/.worktrees/assignee-backend / codex/stage7-notification-outbox-store | rpc/task/status.go、status_test.go，新notification_outbox_store.go/test.go |
| B发布器 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway / codex/stage7-notification-outbox-publisher | 新rpc/task/notification_publisher.go/test.go |
| C协议/迁移验证 | D:/zy/GoLang/go-im/.worktrees/assignee-ui / codex/stage7-notification-outbox-contract-tests | 新internal/model/task_notification_event_test.go、rpc/task/notification_outbox_schema_test.go |
| root | D:/zy/GoLang/go-im / codex/stage7-notification-outbox | 共同model/contract、029/init、进程runtime/main、文档、必要组合验证/兼容夹具、Git/集中测试 |

共同提交后再分支。子agent只编辑允许文件和gofmt/diff检查，不测试/build/提交/合main/push/部署，不调用真实数据库/Kafka/模型。缺接口先向root确认，不能擅改共同文件。集中使用SQL/Kafka替身、全Go回归；如无JS改动不重复既有259项Node。真实SQL行锁/事务、Kafka ACK、进程信号及容器上线仍待统一验收。本批不实现Push消费、TLS入口、WS权限或页面提醒。

## 本批实现与审查

共同d757f80、存储791f808、发布76e8f89、协议验证7caaab6由root保存并无冲突合入codex/stage7-notification-outbox；执行agent没有自行提交或合main。main仍89e2a1e，本批差异基线39c5493，root最终修订保存在整合分支。

| 步骤 | 实际改动、目的及结果 |
| --- | --- |
| 1. 共同契约 | 用户选A69/A70，固定最小事件、通知ID、独立表、029/init与三分支边界；事件不当权限 |
| 2. 事务写入 | 每条去重通知取得正自增ID后立即写Outbox；任一步失败回滚原状态/操作，相同状态无新事件 |
| 3. 存储适配 | 有界升序整批校验；短SQL事务锁原事实，只在未发时写首次数据库发布时间，重放不UPDATE |
| 4. 发布器 | 固定key/payload，整批合法才发，同步ACK后标记；失败本轮停止，间隔再试，允许重复 |
| 5. 协议验证 | 字符串大ID、版本/type、最小字段及init029一致/无历史回填，7个测试函数通过 |
| 6. 进程接线 | 默认关闭、启用检查独立Topic/broker，生产RequireAll同步writer，停机取消/等待/再关闭 |
| 7. 集中验证 | 真实Task状态→GORMstore→发布器故障重建组合配SQL/Kafka替身；首次取消测试清理早于异步回滚，root只修测试等待，10次及最终全仓Go通过 |
| 8. 部署与审查 | 标明029关闭发布也必须迁移、私有Topic匹配及已发布/收到/已读区别；23个文件如下 |

调用链：Gateway既有状态PUT → Task原权限检查 → 同一SQL事务写状态/操作/通知/待发事件 → RPC返回；后台读取Task待发记录 → 同步Kafka ACK → Task短SQL事务核对固定事实并标记首次发布时间。Kafka之后的Push/WS/页面尚未接通，不能将其画为已完成。

新增34个测试函数：事务/store14、发布器7、协议/迁移7、配置/生命周期5、组合1，含子场景。root定向验证及最终 `go test ./... -count=1` 全仓通过，新store超时回滚测试重复10次通过。无JS改动，不重复上一批259项Node；无新依赖，临时Go缓存、构建包并行度1。

状态入口的实际本机TCP gRPC测试已随全仓回归验证新事务接线。组合用生产状态处理器/GORMstore/发布器，但SQL/Kafka/User仍是替身；取消测试等待database/sql异步回滚被观察到，没有删除回滚断言或改生产逻辑。没有实际杀进程、连接真实数据库/broker或执行029；真实时间/事务隔离/并发行锁、Kafka ACK、进程信号、浏览器/容器/云部署待验收。A70已确认但新TLS入口、Push消费、WS权限及页面提醒未实施；默认不开新发布器，published不代表浏览器已收到。

### 全部实际修改文件

相对39c5493共23个文件，无proto/生成代码、依赖、聊天页面或旧Push/WS修改。

| 文件 | 用途 |
| --- | --- |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md) | 029及启用/升级边界 |
| [deploy/mysql/init.sql](D:/zy/GoLang/go-im/deploy/mysql/init.sql) | 新库Outbox |
| [deploy/mysql/migrations/029_task_notification_outbox.sql](D:/zy/GoLang/go-im/deploy/mysql/migrations/029_task_notification_outbox.sql) | 旧库新表，不回填历史 |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A69/A70选择与实施取舍 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 本地成果及下一步 |
| [docs/stage7-notification-outbox-contract.md](D:/zy/GoLang/go-im/docs/stage7-notification-outbox-contract.md) | 共同契约/八步审查 |
| [docs/stage7-notification-realtime-design.md](D:/zy/GoLang/go-im/docs/stage7-notification-realtime-design.md) | 已确认方向及备选 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三分支交付/整合 |
| [internal/model/task_notification_event.go](D:/zy/GoLang/go-im/internal/model/task_notification_event.go) | 最小事件及稳定key |
| [internal/model/task_notification_event_test.go](D:/zy/GoLang/go-im/internal/model/task_notification_event_test.go) | 字段/大ID/非法版本 |
| [rpc/task/README.md](D:/zy/GoLang/go-im/rpc/task/README.md) | 事务、开关、部署说明 |
| [rpc/task/main.go](D:/zy/GoLang/go-im/rpc/task/main.go) | 运行/停机接线 |
| [rpc/task/notification_outbox_contract.go](D:/zy/GoLang/go-im/rpc/task/notification_outbox_contract.go) | Task行和store/writer接口 |
| [rpc/task/notification_outbox_schema_test.go](D:/zy/GoLang/go-im/rpc/task/notification_outbox_schema_test.go) | 初始化/迁移与行契约 |
| [rpc/task/notification_outbox_store.go](D:/zy/GoLang/go-im/rpc/task/notification_outbox_store.go) | GORM读取及原事实确认 |
| [rpc/task/notification_outbox_store_test.go](D:/zy/GoLang/go-im/rpc/task/notification_outbox_store_test.go) | 读取/重试/回滚；root修时序 |
| [rpc/task/notification_publisher.go](D:/zy/GoLang/go-im/rpc/task/notification_publisher.go) | 有界后台同步发布 |
| [rpc/task/notification_publisher_test.go](D:/zy/GoLang/go-im/rpc/task/notification_publisher_test.go) | ACK/确认失败、预检与取消 |
| [rpc/task/notification_publication_flow_test.go](D:/zy/GoLang/go-im/rpc/task/notification_publication_flow_test.go) | 生产组合故障重建验证 |
| [rpc/task/notification_runtime.go](D:/zy/GoLang/go-im/rpc/task/notification_runtime.go) | 开关、Topic与资源生命周期 |
| [rpc/task/notification_runtime_test.go](D:/zy/GoLang/go-im/rpc/task/notification_runtime_test.go) | 配置/等待/关闭验证 |
| [rpc/task/status.go](D:/zy/GoLang/go-im/rpc/task/status.go) | 同事务创建Outbox |
| [rpc/task/status_test.go](D:/zy/GoLang/go-im/rpc/task/status_test.go) | 旧夹具升级及事件失败回滚 |
