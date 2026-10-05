# 阶段7：任务提醒进程启动接线

2026-10-05；基线b7ba626，main89e2a1e。本批只把A69—A72已验证组件接入Push/WS运行，默认关闭，无页面/迁移/依赖/协议变化，不启动真实基础设施。最多八步：共同配置1、Push资源1、WS资源1、配置验证1、Push主进程1、WS主进程1、集中验证1、文档1。每个执行agent只有两个明确文件，不测试/build/Git写入/合main/部署。

## 共同配置

internal/config.Config.TaskNotifications按task_notifications读取，Push和WS角色独立enabled开关，缺字段默认false。Push配置Topic/ConsumerGroup/WSDNSName/Routes/TLS，WS配置ListenAddr/PushDNSName/UserRPCAddr/TLS；TLS为CertFile/KeyFile/CAFile。Push复用cfg.Kafka.Brokers，Topic必须不同于聊天、配置的Agent Topic及默认agent_task_triggers，消费组不得同聊天组；私有Agent组与提醒组仍须部署时分开。WS新端口不能同对外/旧内部端口。每进程只校验自身角色。开关关时不读证书、不建reader/client/User连接/监听；启用但不完整或证书不可读时拒绝启动，不降级。

使用既有私有YAML增加块，不新增环境变量解析层或改旧聊天配置。Routes是Redis已有WS_RPC_ADDR到专用HTTPS origin的映射。例im-ws:9091→https://im-ws:9443；DNS SAN明确配置ws.go-im.internal/push.go-im.internal，URL节点名与证书服务名分别校验。TLS凭证加载在连接基础设施前。User资格检查沿项目既有普通User RPC、当前连接Bearer；不伪造后台用户或复用受限触发接口。

## A：Push运行组件

唯一新增cmd/push/task_notifications.go和task_notifications_test.go，目录D:/zy/GoLang/go-im/.worktrees/assignee-backend，分支codex/stage7-notification-push-runtime。

固定API：prepareTaskNotificationPush(cfg *config.Config, logger *zap.Logger, factories taskNotificationPushFactories)(*taskNotificationPushRuntime,error)。Factories字段client func(push.TaskNotificationClientConfig)(notificationRuntimeSender,error)，reader func(kafka.ReaderConfig) push.TaskNotificationReader；notificationRuntimeSender接口嵌push.TaskNotificationSender及Close() error。默认client为生产TLS客户端，先client成功再reader；Reader同步CommitInterval0、StartOffset kafka.FirstOffset、MinBytes1、MaxBytes1e6、QueueCapacity1，独立GroupID/Topic，复制brokers切片。完整结构配置先校验。reader错误/空值与部分初始化须关闭已建资源。关闭时不启reader、不调用工厂，返回nil,nil。

runtime.Start(ctx context.Context, online push.TaskNotificationOnlineLocator) error只允许一次，构造生产TaskNotificationConsumer并go Run；ErrInvalidTaskNotification仅记固定日志且停止提醒worker，不能取消聊天或自动重启。Start错误不能冒充已运行，nil/取消ctx拒绝。runtime.Close() error应可安全重复：内部取消、等待Run结束，再Close reader/客户端，调用者即使尚未Start或其他worker仍未停止也能清理；多次Close不再调用资源Close。Run退出（包括坏事件）不自动关闭Reader，避免资源关闭时与其他路径竞争。用替身验证开关/初始化顺序、同步reader配置、取消等待、关闭/坏事件不重启。root负责cmd/push/main.go接线及真实RedisRepo，无须改旧startPushWorkers。

## B：WS运行组件

唯一新增cmd/ws/task_notifications.go和task_notifications_test.go，目录D:/zy/GoLang/go-im/.worktrees/assignee-gateway，分支codex/stage7-notification-ws-runtime。

固定API：prepareTaskNotificationWS(cfg *config.Config, hub *ws.Hub, logger *zap.Logger, factories taskNotificationWSFactories)(*taskNotificationWSRuntime,error)。Factories字段tlsConfig func(rpcauth.CertificateFiles,string)(*tls.Config,error)，user func(string)(ws.TaskNotificationTeamChecker,io.Closer,error)，listen func(string,string)(net.Listener,error)。默认TLS为NewNotificationServerTLSConfig，默认user grpc.NewClient + insecure.NewCredentials普通User RPC（当前Bearer验证既定路径，不是Push→WS降级）；默认listen net.Listen。先校验/加载TLS再创建User连接，不在prepare绑定端口；关闭配置不调用工厂或要求hub。启用hub为空拒绝。runtime.Start() error只允许一次，绑定配置专用监听，ServeTLS使用已载入Certificates（不能Serve明文），注册唯一固定路径生产NewTaskNotificationHandler；http.Server设置ReadHeaderTimeout3s/ReadTimeout5s/WriteTimeout5s/IdleTimeout30s/MaxHeaderBytes8KiB。net.Listen同步错误返回，main将退出，不静默忽略。runtime.Errors() <-chan error供root主进程监听异常Serve退出，固定错误不包含地址/证书/User原错；关闭时不报告正常ErrServerClosed。runtime.Close() error幂等：Shutdown限5秒，必要Close强制断连接，等待Serve goroutine退出后关闭User连接；未Start/失败Start也可清理。停止接收提醒后再关闭User，不能立即关闭仍在处理请求的依赖。只管理专用通知server，不修改旧明文server/main/Hub生命周期。测试用实际本机TLS/替身User，覆盖默认关闭/错误配置/证书/重复Start、端口占用、取消关闭顺序和普通HTTP不能进入。root负责旧两个HTTP监听的main信号关闭以及新增runtime接线。

## C：共同配置验证

唯一新增internal/config/task_notifications_test.go，目录D:/zy/GoLang/go-im/.worktrees/assignee-ui，分支codex/stage7-notification-runtime-tests。测试ValidateTaskNotificationPush/WS：默认关闭无需证书/地址，不受另一个角色错误配置影响；启用缺参数/非法broker/混Topic/group/伪HTTPS/未知URL字段/服务名/端口冲突；实际临时YAML通过既有Load映射两角色全部字段及字符串路由。Viper为全局，测试不Parallel且每次Reset，不能泄露临时配置影响别的测试。仅gofmt/diffcheck，不改配置定义/模板/共同文档。

root统一配置/模板/文档/Git与集中测试，现有.bases/certs/私有local.yaml不读取不修改。三个执行worktree都从本批共同提交建立；进程级替身与本机TLS不代表真实MySQL/Redis/Kafka/User容器、生产证书或浏览器验收。默认不开启消费者，因此实际first-offset策略仅对没有既有group offset的新组生效，已有组沿已提交offset恢复；首次开启前需确保独立Topic事件合法。

## 本批实现与审查

主agent从b7ba626建立codex/stage7-notification-runtime，共同7efed33。三个执行agent仅新增本契约指定文件，未运行test/build/Git写或部署。root审查并保存Push383f291、配置测试fe0045b、WS2a787f3及测试修正4b5d3f4，无冲突整合。root负责两个main的真实接线、模板/覆盖、现有HTTP退出测试和仓库模板读取验证。WS首次定向发现新测试变量作用域及切换真实TLS工厂后漏记录顺序的断言问题，root只修测试，保留实际清理要求；修正后WS/Push/配置定向通过。实际工作八步，所有目标都围绕本轮可选提醒启动和清理。

| 步骤 | 实际改动、目的及验证 |
| --- | --- |
| 1 配置与契约 | 独立默认关闭角色、Topic/group/URL/端口/TLS必填校验；本地及容器模板、证书挂载可选覆盖，避免旧部署被自动开启 |
| 2 Push运行 | TLS优先初始化，再独立同步Reader；单次Start、内部取消/等待、幂等资源关闭。替身覆盖坏事件不确认不重启、关闭不取消聊天、部分初始化及并发Close |
| 3 WS运行 | TLS预检后普通User客户端，Start同步绑定专用ServeTLS监听；正常关闭不报故障，异常固定错误。实际本机mTLS离线请求与明文拒绝、端口占用、活动处理器/依赖关闭顺序通过 |
| 4 配置验证 | 实际临时YAML与结构矩阵验证两角色独立、旧无块关闭、URL/服务身份/端口/Topic/group；root追加读取两份仓库模板，补凭证路径后可启用校验 |
| 5 Push主进程 | main接prepare→Redis→通知Start，与原chat/Outbox同父signal context；退出先取消/通知Close/原worker等待关闭，初始化失败也执行defer |
| 6 WS主进程 | main接prepare→Redis/Hub→原HTTP两端口同步绑定→专用TLS Start；主流程监听系统取消/HTTP/TLS错误后清理。root测试原第二端口失败不泄漏第一端口、HTTP活动请求结束后才返回 |
| 7 集中验证 | 各执行包与整合cmd/ws、cmd/push、internal/config定向通过；全仓Go和Linux目标验证结果见下文 |
| 8 文档与审查 | 更新A69—A72实施取舍、计划、协作及部署说明，列全18文件；main未合并，三个执行worktree保留 |

关键调用链：Push加载自身启用配置/证书→生产TLS客户端及独立Kafka Reader→Redis在线查询→白名单HTTPS→WS专用监听/当前连接/User普通RPC资格检查→最小队列提示或offline/denied→同步commit。Task仍负责状态/通知/Outbox事务及独立Topic发布。三个开关（Task环境变量、Push YAML、WS YAML）需最终同步配置；模板不是生产密钥，也不自动执行迁移。

验证范围：定向、仓库模板及`go test ./... -count=1`通过；Linux/CGO0 Push/WS两个目标编译通过、退出码0（默认模块stat缓存写有权限警告，未影响生成目标；没有执行Linux二进制）。没有JS变化，不重复上一批Node。Go测试采用真实本机TLS及生产通知handler/consumer，Kafka/Redis/User资格/客户端及HTTP请求部分使用替身；本批没有连接真实基础设施或模拟实际系统SIGTERM，也没有Docker命令可运行Compose合并/容器验收。新的通知HTTP可取消并等待handler；旧Hub/hijacked WebSocket完整停机排空未实现，不能把HTTP Shutdown当聊天送达保证。通知runtime处理器若不响应取消，关闭将返回固定超时错误，继续延后到handler退出再关闭User，不强制终止Go业务goroutine。页面提示/重复提醒合并/重连查询接线仍待下一批，当前新提醒下行不等于页面已展示或自动已读；真实证书/Topic/迁移/浏览器/云验收留最终统一执行。main仍89e2a1e，未push/部署/调用模型。

全部18个实际修改文件（相对于本批基线b7ba626）：

| 文件 | 作用 |
| --- | --- |
| [config.go](D:/zy/GoLang/go-im/internal/config/config.go) | 接入可选配置块 |
| [task_notifications.go配置](D:/zy/GoLang/go-im/internal/config/task_notifications.go) | 两角色字段与开启校验 |
| [task_notifications_test.go配置](D:/zy/GoLang/go-im/internal/config/task_notifications_test.go) | 临时YAML、边界与实际模板 |
| [Push task_notifications.go](D:/zy/GoLang/go-im/cmd/push/task_notifications.go) | 独立Reader/客户端与生命周期 |
| [Push task_notifications_test.go](D:/zy/GoLang/go-im/cmd/push/task_notifications_test.go) | 初始化/取消/失败及幂等关闭 |
| [Push main.go](D:/zy/GoLang/go-im/cmd/push/main.go) | 启动及系统信号接线 |
| [WS task_notifications.go](D:/zy/GoLang/go-im/cmd/ws/task_notifications.go) | 专用TLS监听/User依赖生命周期 |
| [WS task_notifications_test.go](D:/zy/GoLang/go-im/cmd/ws/task_notifications_test.go) | 实际TLS及监听/关闭故障 |
| [WS main.go](D:/zy/GoLang/go-im/cmd/ws/main.go) | 主流程监听失败与信号处理 |
| [WS main_test.go](D:/zy/GoLang/go-im/cmd/ws/main_test.go) | 原HTTP启动失败清理/活动请求关闭 |
| [go-im.yaml](D:/zy/GoLang/go-im/config/go-im.yaml) | 本地默认关闭模板 |
| [docker-config.yaml](D:/zy/GoLang/go-im/deploy/docker-config.yaml) | 容器默认关闭模板 |
| [docker-compose.notifications.yaml](D:/zy/GoLang/go-im/deploy/docker-compose.notifications.yaml) | Task开关/独立只读证书挂载，不映射外网端口 |
| [部署README](D:/zy/GoLang/go-im/deploy/README.md) | 私有配置匹配与最终验收要求 |
| [project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 实际进度和下一步 |
| [architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | 配置/offset/生命周期取舍 |
| [worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三Agent边界及root修正整合 |
| [本契约](D:/zy/GoLang/go-im/docs/stage7-notification-runtime-contract.md) | 接口/八步/全部文件及验证限制 |
