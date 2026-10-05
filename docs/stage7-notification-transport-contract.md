# 阶段7：Push→WS任务提醒的mTLS传输基础

2026-10-05；A69/A70用户已选，本轮用户再选A71坏事件停止任务提醒消费并保留原偏移量、A72明确离线/撤权不补发提示而查询恢复。基线f6e05db，main89e2a1e。本批实现服务鉴别/发送客户端/WS权限处理器、消费组件及本地验证，不接进程启动配置或页面提醒。最多九小步：规则确认1、共同1、TLS1、WS1、Push客户端1、协议/TLS测试1、消费1、集中验证1、文档1。

## 共同接口

独立HTTPS路径model.TaskNotificationPushPath=/internal/task-notifications，只允许POST，正文固定TaskNotificationEvent，最大1024字节。共同DecodeTaskNotificationEvent严格只五字段，拒绝重复/未知字段、多个JSON、非法UTF8、非规范/数字ID、版本/type错误。与原明文/internal/push无关系。新TLS13配置加载独立证书/CA，双向验证且精确DNS SAN，双方都不能只凭同CA或通配证书通过；HTTP处理器也检查VerifiedChains/握手/允许Push SAN，防止误挂到旧明文入口。

WS API：NewTaskNotificationHandler(hub *Hub, teams TaskNotificationTeamChecker, pushDNSName string) http.Handler。TaskNotificationTeamChecker接口为CheckTeamMember(context.Context,*userpb.CheckTeamMemberRequest,...grpc.CallOption)(*userpb.CheckTeamMemberResponse,error)。只从事件recipient找当前连接；不存在返回HTTP200 {notification_id字符串,outcome:offline}。当前连接Bearer转发User，仅带authorization单metadata，最多2秒；Unauthenticated/PermissionDenied返回200 denied，临时依赖错/无效身份回包/上下文结束返回503或504。返回user_id须正且等于连接UserID及事件recipient，role0—2。成功后在Hub短读锁内再次比较同一连接指针并非阻塞入队最小ServerMsg(type=task_notification_changed,data={version:1,notification_id字符串,team_id字符串})，不发recipient/Token/任务正文；替换连接、连接关闭或队列满返回503，队列接受返回200 queued。此处不调用旧Client.Send的满队列注销/关闭分支，避免持Hub读锁等待注销导致阻塞，也不因提醒拥塞关闭聊天连接。User调用不持Hub锁。响应ID回显原事件。queued仅是写队列接受，非浏览器ACK；按已选A72，offline/denied同样确认消费，通知记录留在Task，重连按当前权限查询。

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

## 本批实现与审查

本批沿A69/A70，用户另行明确确认A71/A72后实施，共九小步。主agent发布共同提交8181f18，三个执行agent仅各自两个文件编辑/格式化；主agent逐个包测试、审查、提交A a491ea0、B 2704cb2、C da8b258，无冲突合入codex/stage7-notification-transport，main仍89e2a1e。

| 步骤 | 实际成果与解决的问题 | 验证 |
| --- | --- | --- |
| 1 规则确认 | A71坏事件停独立消费并保留offset；A72离线/撤权跳过提示，持久通知仍可查询 | 用户两项明确选择，记录候选、理由与代价 |
| 2 共同接口 | 固定五字段事件、字符串ID、1024上限及三种结果，三个worktree统一起点 | 大ID、未知/重复字段、多JSON、版本与长度边界 |
| 3 TLS鉴别 | 双方验证CA并要求精确服务DNS SAN，TLS1.3，无明文降级 | 本机实际HTTPS握手，错误身份/CA/匿名/过期/通配证书拒绝 |
| 4 WS权限 | 当前连接Token→User团队资格→比较同一连接→非阻塞最小提醒 | 当前Token与2秒预算、撤权/身份错、换连接、满队列与注销队列不阻塞 |
| 5 Push发送 | 在线地址只映射到配置HTTPS白名单，不跟重定向，严格回包 | 映射复制/未知地址、非法origin、伪成功/超限/取消、关闭 |
| 6 协议/TLS回归 | 独立检查共同定义与真实服务鉴别，避免只有替身请求成功 | rpcauth/model两个包通过 |
| 7 独立消费 | 当前事件处理和确认完成后再Fetch；临时错重试；坏事件锁定停止 | queued/offline/denied、Redis离线、重复确认、无效事件及超时不确认 |
| 8 组合与全仓验证 | 真实客户端→真实TLS→生产WS处理器offline→消费者确认原offset | Kafka/Redis/User替身；定向Push和全仓Go回归通过 |
| 9 进度与审查记录 | 更新计划、选型、部署限制和全部文件清单 | 差异格式与三个执行worktree状态检查 |

调用链：独立Reader Fetch → 严格解码 → Redis在线地址 → 白名单mTLS客户端 → WS当前连接/User核权 → 最小提示入队或offline/denied → CommitMessages。任务内容和已读状态仍由Gateway→Task带当前本人权限处理。

验证：三个worktree包级测试通过，整合后的通知定向及`go test ./... -count=1`通过。测试包含实际本机TLS握手；Redis/Kafka/User/WS连接队列使用替身，组合测试的WS Hub为空，验证过时在线地址的offline路径，不声称真实浏览器收到了提醒。本批没有页面/协议生成/迁移/依赖变动，不重复上一批Node测试。cmd/push、cmd/ws尚未创建新消费者/监听器，未配置生产证书、真实Topic/服务地址、停机流程或Compose开关；这些是下一批接线范围，当前启动应用不会自动启用任务在线提醒。027/028/029未执行，真实SQL/Kafka/Redis/User进程、并发撤权、浏览器、容器及云端仍待统一验收。queued不是浏览器确认；查询时继续核权，不承诺跨服务原子授权或恰好一次提示。

全部17个实际修改文件（相对于本批基线f6e05db；包含主agent组合验证及文档）：

| 文件 | 作用 |
| --- | --- |
| [notification_tls.go](D:/zy/GoLang/go-im/internal/rpcauth/notification_tls.go) | 专用双向TLS及精确服务身份 |
| [notification_tls_test.go](D:/zy/GoLang/go-im/internal/rpcauth/notification_tls_test.go) | 本机真实握手与错误证书 |
| [task_notification_delivery.go](D:/zy/GoLang/go-im/internal/model/task_notification_delivery.go) | 严格事件解码和回显结果 |
| [task_notification_delivery_test.go](D:/zy/GoLang/go-im/internal/model/task_notification_delivery_test.go) | ID/字段/版本/长度边界 |
| [WS task_notification.go](D:/zy/GoLang/go-im/internal/ws/task_notification.go) | 当前连接核权与非阻塞提醒 |
| [WS task_notification_test.go](D:/zy/GoLang/go-im/internal/ws/task_notification_test.go) | 核权/连接/队列与实际TLS |
| [task_notification_client.go](D:/zy/GoLang/go-im/internal/push/task_notification_client.go) | 受控HTTPS映射与严格响应 |
| [task_notification_client_test.go](D:/zy/GoLang/go-im/internal/push/task_notification_client_test.go) | 客户端失败和安全边界 |
| [task_notification_consumer.go](D:/zy/GoLang/go-im/internal/push/task_notification_consumer.go) | 串行消费、重试与确认规则 |
| [task_notification_consumer_test.go](D:/zy/GoLang/go-im/internal/push/task_notification_consumer_test.go) | 替身验证消费与取消 |
| [task_notification_flow_test.go](D:/zy/GoLang/go-im/internal/push/task_notification_flow_test.go) | 生产TLS/client/handler与消费组合 |
| [project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 实际进度和下一步 |
| [architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A70实现取舍及A71/A72确认 |
| [stage7-notification-realtime-design.md](D:/zy/GoLang/go-im/docs/stage7-notification-realtime-design.md) | 已确认方向与组件进度关联 |
| [本契约](D:/zy/GoLang/go-im/docs/stage7-notification-transport-contract.md) | 精确接口、九步和完整审查 |
| [worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三分支边界及整合记录 |
| [部署README](D:/zy/GoLang/go-im/deploy/README.md) | 尚未启动的监听/消费与验收限制 |
