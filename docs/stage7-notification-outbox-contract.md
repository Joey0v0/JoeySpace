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
