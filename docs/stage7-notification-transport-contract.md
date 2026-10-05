# 阶段7：Push→WS任务提醒的mTLS传输基础

2026-10-05；A69/A70用户已选，本轮用户再选A71坏事件停止任务提醒消费并保留原偏移量、A72明确离线/撤权不补发提示而查询恢复。基线f6e05db，main89e2a1e。本批实现服务鉴别/发送客户端/WS权限处理器、消费组件及本地验证，不接进程启动配置或页面提醒。最多九小步：规则确认1、共同1、TLS1、WS1、Push客户端1、协议/TLS测试1、消费1、集中验证1、文档1。

## 共同接口

独立HTTPS路径model.TaskNotificationPushPath=/internal/task-notifications，只允许POST，正文固定TaskNotificationEvent，最大1024字节。共同DecodeTaskNotificationEvent严格只五字段，拒绝重复/未知字段、多个JSON、非法UTF8、非规范/数字ID、版本/type错误。与原明文/internal/push无关系。新TLS13配置加载独立证书/CA，双向验证且精确DNS SAN，双方都不能只凭同CA或通配证书通过；HTTP处理器也检查VerifiedChains/握手/允许Push SAN，防止误挂到旧明文入口。

WS API：NewTaskNotificationHandler(hub *Hub, teams TaskNotificationTeamChecker, pushDNSName string) http.Handler。TaskNotificationTeamChecker接口为CheckTeamMember(context.Context,*userpb.CheckTeamMemberRequest,...grpc.CallOption)(*userpb.CheckTeamMemberResponse,error)。只从事件recipient找当前连接；不存在返回HTTP200 {notification_id字符串,outcome:offline}。当前连接Bearer转发User，仅带authorization单metadata，最多2秒；Unauthenticated/PermissionDenied返回200 denied，临时依赖错/无效身份回包/上下文结束返回503或504。返回user_id须正且等于连接UserID及事件recipient，role0—2。成功后在Hub短读锁内再次比较同一连接指针并发送最小ServerMsg(type=task_notification_changed,data={version:1,notification_id字符串,team_id字符串})，不发recipient/Token/任务正文；替换连接或Send失败返回503，队列接受返回200 queued。User调用不持Hub锁。响应ID回显原事件。queued仅是写队列接受，非浏览器ACK；offline/denied只是事实，尚未决定消费确认。

Push API：TaskNotificationClientConfig {Files rpcauth.CertificateFiles, WSDNSName string, Routes map[string]string}；NewTaskNotificationClient(cfg)(*TaskNotificationClient,error)，Send(ctx context.Context,onlineAddr string,event model.TaskNotificationEvent)(model.TaskNotificationDelivery,error)，Close() error。Routes将Redis已有在线地址（如im-ws:9091）映射为受控HTTPS origin（如https://im-ws:9443），构造验证host/port、无user/path/query/fragment；不得直接信任Redis地址拼任意URL、猜测明文降级或跟随重定向。只访问映射节点固定路径，服务名用配置的精确WS SAN。单次3秒、响应最多1024，校验HTTP200、JSON完整/无未知字段、回显ID/允许outcome；任何HTTP403/400/503/网络/无效响应都是安全临时传输错误，不能伪装业务denied或成功。无用户Token，不操作Kafka/Task数据库，Close仅关闭闲置连接，真正运行时仍须先取消/等待消费。

## 三worktree分工

| 执行 | 绝对目录及分支 | 唯一允许文件 |
| --- | --- | --- |
| A WS权限处理器 | D:/zy/GoLang/go-im/.worktrees/assignee-backend / codex/stage7-notification-ws-handler | 新internal/ws/task_notification.go、task_notification_test.go |
| B Push发送客户端 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway / codex/stage7-notification-push-client | 新internal/push/task_notification_client.go、task_notification_client_test.go |
| C TLS/协议验证 | D:/zy/GoLang/go-im/.worktrees/assignee-ui / codex/stage7-notification-transport-tests | 新internal/rpcauth/notification_tls_test.go、internal/model/task_notification_delivery_test.go |
| root | D:/zy/GoLang/go-im / codex/stage7-notification-transport | 共同TLS/model定义、共同文档、必要组合验证和集中Git/测试 |

子agent只编辑/gofmt/diffcheck，不测试/build/提交/merge/main/push/部署。缺接口先找root，不改旧WS/Push/main/协议/依赖/迁移。root先保存共同提交，三分支同起点；测试使用真实本机HTTP/TLS证书握手，User及连接队列为替身，不声称生产证书/浏览器/Kafka/Redis已验收。没有授权启动真实云端或模型。

## 本轮新增消费组件（root负责）

TaskNotificationConsumer使用独立Reader/Topic，逐条Fetch，不在原事件处理＋确认完成前取更高offset。严格校验Topic/非负partition和offset、Key与DecodeEvent；异常返回固定错误并停止该Run，未确认，不静默跳过/自动重启，不影响独立聊天消费。先查Redis在线地址，空地址为明确offline；非空调用受鉴别客户端，queued/offline/denied才进入commit阶段；User/Redis/网络/队列满等临时错间隔1秒重试同事件。发送成功后commit失败只重试commit，重启可能重发同ID，页面后续合并提醒。各IO有总超时，ctx失效不commit，CommitMessages同步3秒确认。任务通知始终保存在Task，不建立聊天离线记录或额外待提醒表。本批消费者组件可以在本地测试运行，但不被cmd/push自动启动；生产Reader/Redis配置及可选TLS监听生命周期在下一批接进程。
