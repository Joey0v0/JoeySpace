# Docker Compose 部署配置基线

2026-10-06 退出资格基础（A75/A76/A77全部A已确认）：升级User前，已有库先核对001已执行，再执行一次031_team_membership_lifecycle.sql，原成员默认active（0）/generation=1。User资格、目录/候选与后台触发查询仅认active；generation新增RPC字段不得在后续版本授权中缺失时猜成1。032_im_team_group_fences.sql为IM持久关闭事务组件的表，无初始撤权或数据清理。两份迁移均尚未执行，不重跑ALTER；新init现包含teams/team_members及生命周期字段，重跑001的IF NOT EXISTS不会更改这些新字段，旧库仍用031。部署时协调新User协议/查询，不在尚未迁移的数据库运行新版User。

资格基础批次未开放退出/恢复/清理RPC，也没有自动后台恢复或新私有TLS配置。后续写入保护批次已将032关闭记录接入IM的Join/Create写入：先升级并迁移User（001已存在、031已执行），再执行032、升级IM；新版IM拒绝缺本人/正generation的旧User响应，不能先混跑旧User副本。现有入群/建群查询会访问032表，未执行032时请求失败；只更新init不会升级旧卷。旧IM的groups原始SQL引用已修复，但尚未用真实MySQL验证。该写入批次仍无退出/恢复/清理RPC或Push核权；随后普通读取接线见下一段。保留原数据卷、消息与已读记录。[写入批次契约与结果](../docs/stage7-team-group-write-guard-contract.md)。

后一批已让普通本人团队群读权限（包括历史、未读、来源核查、机器人回帖经普通入口、旧离线拉取）在User成功回显本人/正generation之后，再由IM同一SQL快照复核当前群成员和032关闭记录；非团队旧群仍按原成员规则。旧有成员未建立关闭行时按0处理；已关闭代际不授权。新版IM普通团队群读取因此也需要先执行032并升级User，不能混用缺generation的旧User实例；031/032真实执行及数据库并发仍待验收。[普通读取审查](../docs/stage7-team-group-read-guard-contract.md#本批实现与审查)。

专用Agent后台触发上下文随后接入同一A75代际规则：UserTrigger资格响应新增field3真实正generation；IM从持久Outbox和原消息获取范围，在读取历史前后要求User同版本并重新核对IM当前群成员/032关闭记录。旧User成功响应缺该字段会被新版IM拒绝；升级顺序仍是核对001、执行031、升级所有User实例，再执行032、升级IM实例，不让旧User副本处理新版调用。没有新增端口/证书/迁移，原有Agent→IM与IM→User专用mTLS配置仍须准备。[后台触发审查](../docs/stage7-trigger-generation-guard-contract.md#本批实现与审查)。

负责人姓名解析的后续小步也接入代际核验：User在候选查询前后要求同一真实正generation；IM在受保护触发上下文返回后、候选解析前保存User版本，候选解析后要求版本未变，再核对IM当前群成员/032关闭边界。旧User/未迁移表不降级放行；升级顺序及原mTLS身份不变，没有新迁移或配置。真实MySQL/证书/Compose/云端仍未验收，团队退出/恢复/清理和Push核权仍需后续接线。[负责人解析审查](../docs/stage7-trigger-resolver-final-guard-contract.md#本批实现与审查)。

A75退出操作存储基础增加[033_user_team_leave_operations.sql](mysql/migrations/033_user_team_leave_operations.sql)：已有User库先核对001和031，再执行033一次；新库init已包含同表。033只建固定请求键/版本/状态记录，不会自动退出成员或清理群，现阶段也没有业务接口写入该表。后续启动真正的退出接口前必须确认033已执行；不能用重跑init升级旧数据卷。DDL与init已静态核对一致，但尚未在真实MySQL执行或验锁。[存储契约](../docs/stage7-team-leave-operation-schema.md)。

后续本地批次已加入User包内退出意图函数，未来入口调用时会在同一事务把成员置为leaving并写033操作；目前未注册RPC/HTTP，生产启动不会自动调用。公开入口启用前，已有库仍须先执行033并完成真实MySQL验证；IM清理、本人显式恢复与Push核权尚未接线。[本批审查](../docs/stage7-team-leave-intent-contract.md#本批实现与审查)。

IMLeave专用清理监听已接入IM进程，但默认关闭；仅完整提供`IM_LEAVE_LISTEN_ON`、`IM_LEAVE_TLS_CERT_FILE`、`IM_LEAVE_TLS_KEY_FILE`、`IM_LEAVE_TLS_CA_FILE`才启动，监听端口须不同于普通IM/Bot/Trigger，客户端证书须为`user.go-im.internal`。当前基础Compose没有注入这些变量、证书挂载或独立端口配置；不能在未执行032的旧库启用。User持久操作调用、Push核权和本人退出入口尚未接线，故这仍不是可操作的退出链。[监听审查](../docs/stage7-team-leave-im-runtime-contract.md#本批实现与审查)。

2026-10-06 A73本人团队群未读：已有库先执行一次030_im_group_message_reads.sql，再升级IM RPC与Gateway；仅更新init不会升级已有卷。新表默认没有阅读记录，当前可读历史中非本人普通消息首次均未读；机器人消息也计入。不新增环境开关，不删除离线投递或旧通知。030尚未执行。页面先登录、填写团队/群并选择群聊，可手动查询计数；加载群历史后点击“将本次已加载历史页标为已读”，仅提交这页确切ID，不自动确认WS/离线/历史读取。权限/账号失效清计数与目标，普通失败可本人刷新或重试；提交后失败不保证没有写。[原实现与全部文件](../docs/stage7-team-group-unread-contract.md#本批实现与审查)。

历史恢复入口已补：“刷新最新群消息”保留正在翻的旧位置；若旧分页已结束但仍有较小ID迟到消息，点击“从最新消息重新遍历”，有效成功后再点加载更早消息逐页寻找。失败/非法或旧账号/群回包不重置原位置，重新遍历不自动标已读，也不提供数据库快照。确认结果不确定时可先查计数、再本人显式重试原页确切ID，查询为0也不会擅自丢弃待确认集合。生产IM TCP恢复定向及353项Node通过；真实MySQL首次时间/重启、030迁移、浏览器/容器/云仍未验收。[本批七步与全部文件](../docs/stage7-group-unread-recovery-contract.md#本批实现与审查)。

Makefile的run-api/run-ws/run-push现以包运行，包含重构后的全部辅助Go文件；三个`go run ./cmd/{api,ws,push} -h`仅验证编译与帮助入口，通过但没有启动服务或读取真实数据库。

阶段7Task通知事件发布第一批（2026-10-05）：新Task在状态变化事务内同时写通知与Outbox，升级前须先027、028、029，029不回填旧通知；关闭发布也必须029。首次初始化已包含新表，现有数据卷不会因改init自动迁移。发布开关 `TASK_NOTIFICATION_PUBLISH_ENABLED` 默认false；显式启用需Task进程的 `TASK_NOTIFICATION_KAFKA_BROKERS` 和独立 `TASK_NOTIFICATION_TOPIC`，容器网络broker为kafka:19092。私有聊天/Agent Topic改名时，同时传 `TASK_NOTIFICATION_CHAT_TOPIC`/`TASK_NOTIFICATION_AGENT_TOPIC` 做隔离校验。基础Compose本批未注入发布配置，最终通过私有覆盖或下一批完整部署覆盖注入；未配置不能宣称已发布。published只表示Kafka确认。029尚未执行，真实Kafka/MySQL、证书、容器与云端仍待验收。[Task契约和审查](../docs/stage7-notification-outbox-contract.md)。

阶段7提醒传输第二批已完成独立Push消费组件、HTTPS白名单客户端、双向TLS配置与WS当前连接权限处理器，本机真实TLS和全仓Go测试通过；当时尚未接进程，组件范围见[传输审查](../docs/stage7-notification-transport-contract.md#本批实现与审查)。第三批运行接线见下文，第四批页面提示/重连查询已完成本地替身验证，见[页面审查](../docs/stage7-notification-realtime-page-contract.md#本批实现与审查)。

## 阶段7任务提醒运行接线

`cmd/push`、`cmd/ws`已接入可选通知运行组件。现有/缺少新块的配置仍默认关闭；`config/go-im.yaml`及`docker-config.yaml`模板提供`task_notifications.push`、`task_notifications.ws`两角色，启用时分别检查自身配置，不要求另一角色同时在本进程可用。Push使用已有`kafka.brokers`，但必须独立Topic及消费组；Task发布Topic必须与Push相同，且不同于聊天/Agent，私有消费组也须彼此隔离。新组从Topic最早保留事件读取，已有组从提交offset恢复；029不回填旧通知。

最终部署时，在现有**私有**`docker-config.local.yaml`添加模板新块，显式启用两个角色；保留原DSN、JWT、聊天/Agent配置。容器示例映射为`im-ws:9091`→`https://im-ws:9443`，WS内部监听`0.0.0.0:9443`，User普通RPC`user-rpc:9001`；左侧必须等于WS现有`WS_RPC_ADDR`写入Redis的地址。证书服务DNS配置为`ws.go-im.internal`/`push.go-im.internal`，不是任意容器名；两服务使用独立私钥，CA信任与精确SAN、ServerAuth/ClientAuth用途和有效期必须匹配。容器内路径按模板填写，真实证书不入库。

可选覆盖`docker-compose.notifications.yaml`为两个容器只读挂载独立证书目录，为Task启用发布及Topic环境变量，**不替你开启私有YAML的Push/WS开关**，也不新增宿主机端口或修改基础Compose。设置本机`WS_NOTIFICATION_CERT_DIR`、`PUSH_NOTIFICATION_CERT_DIR`，目录各含cert.pem/key.pem/ca.pem；发布Topic若改名须同时改私有Push配置。若还使用bot/trigger覆盖，按既有部署清单一并合并。最终先核对执行027—029，再升级相关镜像；只更新init.sql不会升级已有数据卷。

当前未执行以下检查，供最终同步时使用：

```sh
docker compose --env-file .env -f docker-compose.yaml -f docker-compose.notifications.yaml config --quiet
```

启动流程先验证结构并加载TLS材料，再准备其他基础设施；不开启时不读证书、不建通知Reader/User连接或监听。新WS监听固定`/internal/task-notifications`且只接mTLS，绑定失败返回启动错误，不降级到旧`/internal/push`。Push坏事件只停通知worker、保留未确认offset，聊天继续；修复后需重启消费，不能默认跳过坏事件。明确离线/失效Token/离队确认消费，记录仍在Task，不保证补在线提示。停止时通知worker取消/等待后关Reader/客户端，WS停止新HTTP请求并结束通知处理后关User连接。旧聊天Hub与已升级WebSocket没有新增完整逐连接排空机制，HTTP Shutdown不等于客户端已收到消息；本批不声称整套聊天停机已验收。

本地运行组件及TLS测试不等于真实Kafka/Redis/User容器或系统信号验收；当前环境没有Docker命令，Compose合并、证书挂载和真实部署待最终验证。[运行接口和审查](../docs/stage7-notification-runtime-contract.md)。

页面已接入任务提醒：先登录并填有效团队，点击连接；每次成功连接会独立查询本人最新通知，忙时排队一次。新WS提示只显示固定“请刷新通知”，最多128项内存去重；详情和逐条已读仍经Gateway→Task核对当前权限，不自动标已读。后来填写团队可手动刷新或重新连接，页面没有自动连WS、重连或轮询。全部303项Node页面测试及Gateway API测试通过，使用VM/DOM/HTTP/WS替身；真实浏览器与部署链仍待最终验收。[七步、全部文件及使用边界](../docs/stage7-notification-realtime-page-contract.md#本批实现与审查)。

2026-10-05收尾验证新增生产发布器→Push消费、专用mTLS→实际本机WS帧及生产Task TCP gRPC已读丢响应/权限恢复组合，全仓Go通过；SQL/Kafka/User等仍为替身，没有生产配置或代码变更。[六步审查](../docs/stage7-notification-flow-contract.md#本批实现与审查)。最终按[阶段7验收清单](../docs/stage7-acceptance.md)核对全部覆盖文件、迁移、证书与真实业务/故障证据，不能将本机分段测试作为部署成功。

2026-10-05日志修复：User、IM、Task、Agent在构造go-zero RPC时自动抑制统计日志的业务请求正文，旧私有YAML无需新增字段；保留原统计配置及慢调用耗时记录。最终须升级这四个服务的镜像才生效，本批未部署、不清理旧日志，也不代表HTTP/SQL等其他日志全面脱敏。真实框架本机对照及全仓Go通过，[八步与全部文件](../docs/stage7-experience-gap-design.md#本批实现与审查)。

上一批c6394e8的A74离线核权只接IM RPC：Gateway原离线入口随IM升级按当前群/团队资格过滤；明确拒绝群保留投递，临时核权故障读取失败，不自动删除或标已读，其他有权聊天保留。当时旧Gin `/api/v1/message/offline` 仍直查本人投递；后续收口结果见下一段，不能将本地修复视为真实部署已验收。

后续旧离线转发批次已沿A22/A74收口并通过本机组合测试：`cmd/api`复用IM业务RPC连接，GET离线/POST ACK转发原Token，不回落旧库；旧路径、HTTP200业务code、字符串ID/机器人格式保持。ACK增加32KiB/单JSON限制，超长或尾随JSON拒绝。直接运行API默认连接`localhost:9002`，可由`IM_RPC_ADDR`覆盖；基础Compose已给im-api配置`im-rpc:9002`并按service_started安排启动。仅此依赖不保证IM就绪，IM不可用时旧离线入口失败，注册登录等旧功能不因RPC惰性建连就被判不可用。最终需同时升级旧API与IM并使其JWT配置一致，不将仅更新旧API镜像称为已核权。读取/ACK每次3秒上限继承请求取消，不新增应用重试循环；不扩旧API为完整优雅停机。[本批契约和验证边界](../docs/stage7-legacy-offline-contract.md)。静态YAML检查通过，实际Compose启动/挂载及云端联调仍待最终统一验收。

2026-09-20 根据用户提供的云端 Compose、Dockerfile 和服务配置同步，并在本地补入阶段 1 的 API Gateway 与用户 RPC 容器配置。阶段 3 又补入 IM RPC 的容器接线；阶段 4 增加 Task RPC；阶段 5 准备 Agent RPC 的可选容器配置。这里只表示本地配置和程序构建检查，未连接云服务器、未重新部署、未完成真实数据库或模型联调。

2026-09-25 用户决定先完成本地开发与自动化测试，项目整体完成后统一同步云端；以下联调顺序留待届时执行。

## 同步内容

- MySQL 使用 `mysql:8.0`，新增 `mysql_data:/var/lib/mysql` 持久卷。
- Kafka 固定为 `apache/kafka:3.9.2`，同步集群 ID、`-Xms256m -Xmx384m` 内存配置和 `kafka_data` 持久卷。
- 宿主机映射旧 API 8080、WebSocket 8081 和新增 API Gateway 8082；MySQL、Redis、Kafka、WS 内部 9091、用户 RPC 9001、IM RPC 9002、Task RPC 9003、Agent RPC 9004 不发布宿主机端口。
- WS 容器设置 `WS_RPC_ADDR=im-ws:9091` 与 `IM_RPC_ADDR=im-rpc:9002`。
- Docker 构建保留 Go 1.26.1，同步 `GOMAXPROCS=2`、`GOFLAGS=-p=1` 和 Go 模块代理。
- 容器配置使用 `mysql:3306`、`redis:6379`、`kafka:19092`；`config/go-im.yaml` 仍是本地直接运行配置，本次未修改。
- 新 API Gateway 通过 `user-rpc:9001` 调用用户 RPC；用户 RPC 通过 `mysql:3306` 查询现有用户表。
- WS 群消息发送前通过 `im-rpc:9002` 查询旧群成员资格；IM RPC 通过 `mysql:3306` 查询旧 `group_members`。
- Gateway 通过 `task-rpc:9003` 创建任务；Task RPC 通过 `user-rpc:9001` 核对团队资格，任务表仍在开发期共用的 `go_im` 实例中。
- Agent RPC 通过 `im-rpc:9002` 和 `task-rpc:9003` 只读访问授权数据；模型接入点和预算未定，因此容器属于需显式启用的 `agent` profile，默认 Compose 启动不会运行它。

## 凭证与云端原文件的区别

不将聊天中出现的实际密码、JWT 密钥写入仓库。采用以下分工：

| 文件 | 作用 | 是否入库 |
| --- | --- | --- |
| `docker-config.yaml` | 脱敏服务配置模板，含 `CHANGE_ME_*` 占位值 | 是 |
| `docker-config.local.yaml` | 填写实际数据库密码、JWT 密钥后的服务配置 | 否 |
| `.env.example` | MySQL 容器密码、JWT 密钥、各写入服务节点号及空白方舟变量示例 | 是 |
| `.env` | Compose 使用的实际凭证；启用 Agent 时还需填入 `ARK_API_KEY`、`ARK_MODEL_ID` | 否 |

Compose 中三个 Go 服务挂载的源文件因此改为 `./docker-config.local.yaml`，容器内仍是 `/app/config/go-im.yaml`。这是一项有意保留的脱敏差异。Go 旧配置加载器不会自动展开 YAML 中的 `${变量}`，所以服务配置使用私有文件，而不是不可生效的变量占位。

需要使用这套配置时，先在 `deploy` 目录执行（不要覆盖已有私有文件）：

```powershell
# Windows PowerShell
if (-not (Test-Path .env)) { Copy-Item .env.example .env }
if (-not (Test-Path docker-config.local.yaml)) { Copy-Item docker-config.yaml docker-config.local.yaml }
```

Linux 上对应命令：

```sh
test -e .env || cp .env.example .env
test -e docker-config.local.yaml || cp docker-config.yaml docker-config.local.yaml
```

在自己的编辑器里填写 `.env` 和私有 YAML；DSN 使用的密码应与数据库实际密码一致，`.env` 的 `JWT_SECRET` 应与 `docker-config.local.yaml` 的 `jwt.secret` 一致。若接入已有部署，保留现有 JWT 密钥，避免意外使登录 Token 失效。已有 MySQL 数据卷的密码不会仅因修改 `.env` 自动改变。

Compose 的密码变量使用必填表达式，未填写时会报错。`.env` 单引号可用于保留密码中的字面 `$` 等字符，格式说明见 [Docker 官方文档](https://docs.docker.com/compose/how-tos/environment-variables/variable-interpolation/)。不要把完整凭证配置输出后贴到聊天；可用以下命令只检查配置、不启动服务：

```sh
docker compose --env-file .env -f docker-compose.yaml config --quiet
```

私有配置同时被 `.gitignore` 和根目录 `.dockerignore` 排除，避免随 Git 或 Docker 构建上下文带入镜像。本次未创建含真实凭证的私有文件。

## 已同步的 WS 容器地址逻辑

云端 `internal/ws/server.go` 已核对，并于 2026-09-20 同步到本地：设置 `WS_RPC_ADDR` 时使用该地址，未设置时回退为 `localhost:<RPCPort>`。Compose 中的 `im-ws:9091` 因此会被 WS 注册到 Redis，供 Push 容器访问；本机直接运行仍使用回环地址。该逻辑已通过单元测试，但本次尚未在 Docker 或云端做 Push → WS 运行联调。

## 阶段 1 新链路的容器配置

Compose 新增两个服务：

| 服务 | 容器内地址 | 宿主机入口 | 作用 |
| --- | --- | --- | --- |
| `api-gateway` | `api-gateway:8082` | `8082` | 接收新的 HTTP 请求，并调用用户 RPC |
| `user-rpc` | `user-rpc:9001` | 不发布 | 验证 Token，并查询现有 MySQL 用户表 |

对应调用链为：

```text
客户端 :8082 → api-gateway → user-rpc:9001 → mysql:3306
```

新增服务使用独立的 [api-gateway.yaml](api-gateway.yaml) 和 [user-rpc.yaml](user-rpc.yaml)。数据库 DSN 与 JWT 密钥通过 Compose 环境变量注入用户 RPC，实际值只保存在不入库的 `.env` 中。用户 RPC 只供 Compose 内部服务访问，因此不向宿主机发布 9001 端口。

## 阶段 3 IM RPC 的容器接线

Compose 中 `im-ws` 使用 `IM_RPC_ADDR=im-rpc:9002`，API Gateway 也通过独立的 `IMRPC` 客户端调用同一内部服务；`im-rpc` 使用 [im-rpc.yaml](im-rpc.yaml) 监听容器网卡，并读取 `IM_MYSQL_DSN` 与 `IM_JWT_SECRET`。这两个环境变量从同一份不入库的 `.env` 展开，其中 JWT 密钥必须与旧登录配置一致。IM RPC 通过 `USER_RPC_ADDR=user-rpc:9001` 核对团队权限，使用独立 Snowflake 节点 3 创建团队群。IM RPC 端口仅在 Compose 内部可达；服务不可用时 WS 群消息和团队群创建都会拒绝。此配置尚未做容器启动和真实数据库联调。

## 验证范围与下一步

Agent RPC 的构建目标为 `agent-rpc`，使用 [agent-rpc.yaml](agent-rpc.yaml) 监听容器内 9004；Compose 注入 IM/Task/User 内部地址及 Agent 草稿表所用 MySQL DSN，并从不入库的 `.env` 注入方舟凭证。`ARK_API_KEY` 和 `ARK_MODEL_ID` 在模板中留空，以便没有模型配置时仍能解析其他服务的 Compose 文件。只有确认实际接入点与预算后，才可显式选择 `agent` profile 启动；缺少凭证时进程会在监听前退出。本步未运行 Docker，也未请求方舟。

阶段 6 的 Agent 草稿存储已准备[010 建表迁移](mysql/migrations/010_agent_runs_and_drafts.sql)与[011 请求去重迁移](mysql/migrations/011_agent_run_request_key.sql)，用于已有 `go_im` 数据库；新库初始化 SQL 已包含两表与请求唯一索引。Compose Agent 已配置 MySQL、User RPC 和独立的 `AGENT_SNOWFLAKE_NODE_ID`（默认 5），提供 `PrepareTaskDraft` 和只读 `GetTaskDraft` RPC。启动前已有库须按实际结构依次执行尚未执行的迁移；脚本不能当成可重复执行的初始化脚本。此处仅准备配置与脚本，未在真实 MySQL 执行，也未请求真实模型。

2026-10-02 已接入草稿文字编辑及同步确认 RPC，确认复用既有 Task 地址。已有库在启动更新后的 Agent 前还须执行[012 任务结果迁移](mysql/migrations/012_agent_draft_task_result.sql)，草稿读取也依赖其中的创建键与任务 ID 字段；新库定义已包含。确认采用本人显式重试，没有后台自动创建任务，HTTP/页面确认入口已接入并展示任务 ID。仅本机 TCP gRPC 与 SQL 替身测试通过，迁移未执行；详见[确认设计](../docs/agent-confirmation-design.md)。

Gateway 的 [api-gateway.yaml](api-gateway.yaml) 已配置非阻塞的 `agent-rpc:9004` 客户端，但 Compose 不将可选 Agent 设为 Gateway 的启动依赖；未启用 `agent` profile 时，问答请求应返回 503，其他 HTTP 入口仍可工作。这一行为只有配置和本地替身测试，容器网络尚未验收。

阶段 4 的 Task RPC 使用 [task-rpc.yaml](task-rpc.yaml) 在容器内监听 9003，Gateway 的 `TaskRPC` 指向 `task-rpc:9003`。Compose 注入 `TASK_MYSQL_DSN`、`USER_RPC_ADDR=user-rpc:9001`、`IM_RPC_ADDR=im-rpc:9002` 和默认节点号 4；已有数据库须依次执行 [006_tasks.sql](mysql/migrations/006_tasks.sql)、[007_task_operations.sql](mysql/migrations/007_task_operations.sql)、[008_task_source.sql](mysql/migrations/008_task_source.sql) 和 [009_task_due_at.sql](mysql/migrations/009_task_due_at.sql)，再启动更新后的任务 RPC。IM RPC 校验来源消息后，Task RPC 才保存来源 ID；无来源任务不依赖 IM 调用。本机已做本地测试与编译检查；没有启动容器或验证真实数据库。

阶段 7 的任务状态通知落库要求已有数据库在升级 Task RPC 前另执行一次 [027_task_status_notifications.sql](mysql/migrations/027_task_status_notifications.sql)；新库初始化已包含该表。真实状态变化会在同一事务写状态、操作记录和个人通知依据；未迁移时整笔状态变更会失败并回滚。Task 和 Gateway 已提供按团队读取本人通知的只读接口，需同时更新两者以使用新增 RPC。Gateway `/demo/chat` 已接通知面板及两个固定同源嵌入脚本，重新编译Gateway即可随程序更新，无新增前端构建或部署服务；本人填写Token/团队后手动读取。已读扩展见下一段，实时提醒尚未接入。027及真实MySQL/浏览器行为未在本机执行，[查询契约](../docs/stage7-notification-read-contract.md)、[页面基础审查](../docs/stage7-notification-page-contract.md#本批实现与审查)。

A68本人逐条已读需要在027后另执行一次 [028_task_notification_read.sql](mysql/migrations/028_task_notification_read.sql)，再升级Task和Gateway（页面脚本随Gateway嵌入）；新库init已包含read_at。历史记录默认未读，点击保存首次数据库时间，重试不重写；GET依旧不写状态。缺028时新版通知查询/确认会安全报数据库不可用，不自动补表或绕过。当前没有实时提醒或全库未读数。028尚未执行，最终部署需验证升级前旧数据/升级后重复点击/离队/双页面读取，[已读契约和实际限制](../docs/stage7-notification-read-state-contract.md)。

本地配置已把 `api/`、`rpc/user/`、`rpc/im/`、`rpc/task/`、`cmd/agent/` 加入 Dockerfile 和 Compose；各服务的 Linux 交叉编译结果以对应开发步骤记录为准。

本机未发现可用 Docker，因此仅做 YAML 解析、配置字段、Linux 编译和 Go 测试，尚未运行 `docker compose config`、构建镜像或启动容器。同步到云端后，应先核对现有 Compose 项目名和卷，保留已有数据；不要执行 `down -v` 或数据迁移。

该 Compose 现在是云端容器网络基线；原 `make env-up` 不再向 Windows 主机开放中间件端口。若要在主机直接运行 Go 服务，需要另行准备仅绑定回环地址的本地端口覆盖配置，不能把本次配置同步当成本地环境已可启动。

阶段 3 的团队群字段变更见 [003_group_team_id.sql](mysql/migrations/003_group_team_id.sql)。旧数据卷不会因修改 `mysql/init.sql` 自动获得该字段；统一部署前需核对现有表与备份，再单独执行增量迁移。当前仅提供脚本，未在真实 MySQL 执行。

## 阶段 6 机器人存储与双向 TLS 前提

2026-10-02 按用户确认的 A43/A44 增加机器人存储模型和消息身份字段，准备[013 增量迁移](mysql/migrations/013_im_bots_and_message_sender.sql)，新库初始化已同步。更新消息写入服务前，已有库须核对备份、此前迁移与 013；特别是 Push 的消息存储已依赖 `sender_type`、`initiator_id`，仅更新程序而不迁移会导致写入失败。旧消息由默认值保持用户身份，迁移不插入机器人资料或用户账号。本轮未连接真实 MySQL、未执行迁移。

[双向 TLS 凭证加载](../internal/rpcauth/mtls.go)已通过临时测试证书与真实本机 gRPC 验证，但尚未接入 IM/Agent 生产启动、Compose 或机器人 RPC。当前没有可使用的机器人回帖监听地址；不应将此步骤当作启用了回帖或已加密所有 RPC。后续接线会单独说明证书签发、CA 与精确 Agent 身份、私钥挂载及更新方式；凭证加载使用文件快照，当前不提供热更新。测试证书会随测试临时目录清理，不可用于部署。完整范围见[本轮实现记录](../docs/agent-group-reply-design.md#本轮已实现前提与审查文件2026-10-02)。

## 云端联调顺序（待实际执行）

### 群内 @AI 后台触发（可选）

当前本地增加 [docker-compose.trigger.yaml](docker-compose.trigger.yaml) 作为显式覆盖文件：仅在选择 `agent` profile 并提供方舟凭证后，为 User 和 IM 开启容器内部专用 mTLS 监听，并让 Agent 消费独立 Kafka 通知、运行后台草稿 worker。四个触发证书目录在 [.env.example](.env.example) 中留空，必须使用各自私有目录；不要与机器人回帖证书共用私钥。Push 的发布开关只从不入库的 `docker-config.local.yaml` 读取，启用时还须将 `kafka.agent_trigger_enabled` 改为 `true`。基础 Compose 和公开 YAML 模板保持默认关闭。具体启动、迁移、原消息入口和恢复核对见[阶段6运行验收准备](../docs/stage6-runtime-acceptance.md)；目前仅做本地静态配置与自动化测试，尚未运行 Docker 或真实模型。

### 可选 IM 机器人 TLS 入口

IM 进程提供专用机器人监听，Agent 已接入独立 mTLS 客户端、持久回帖意图和同步回帖/显式重试 RPC；Gateway 与原生页面已有状态展示和只重试原回帖的操作。基础 Compose 不启用；[docker-compose.bot.yaml](docker-compose.bot.yaml)作为显式覆盖文件，在 IM 容器内监听 9005，不发布宿主机端口，并分别只读挂载 IM 与 Agent 的证书目录。Agent 保留基础文件的 `agent` profile，仍须显式启用；真实模型配置及预算未就绪前不启动。HTTP 成功返回任务 ID 不代表群卡片已送达；回帖状态须另行查看，失败先重读同一运行再手动重试，见 [HTTP 说明](../api/README.md#回帖状态与显式重试2026-10-03)。

启用前依次核对已有迁移与 [013](mysql/migrations/013_im_bots_and_message_sender.sql)，再执行一次 [014](mysql/migrations/014_im_bot_sends.sql)及 [015](mysql/migrations/015_agent_task_replies.sql)；新数据库初始化已包含两侧记录表。IM 保存发送依据和 Kafka 受理结果，Agent 保存从已成功任务构造的固定回帖及自身收到的受理结果，二者不跨表读写。没有执行真实迁移，不删除发送记录或旧消息来处理重复。

2026-10-04 逐项回帖升级：IM 更新前须核对一次 [020](mysql/migrations/020_im_bot_send_items.sql)，给 IM 发送表追加默认 0 的项序号；Agent 更新前须核对一次 [021](mysql/migrations/021_agent_task_reply_items.sql)，给 Agent 回帖表追加默认 0 的项序号并改为 `(run_id,item_index)` 主键。原消息唯一键和旧第 0 项记录保留；013—019 等前置迁移按现有库实际版本核对，勿重复执行。新初始化已包含两侧项列/主键。先准备对应表，再协调升级 IM、Agent 和 Gateway；新专用 PostTaskCreatedCardItem 对旧 IM 返回 Unimplemented，禁止回退旧方法。本轮已接 Agent 集合逐项确认/回帖及 Gateway 重试，仍不改变 Compose 或启用真实环境；迁移与部署均未执行。[Agent 契约](../docs/multi-reply-agent-contract.md)、[本轮验证范围](../docs/multi-reply-agent-review.md)。

在 IM 所用数据库先查询 `im_bots` 是否已有选定的 `code`。若没有，选择未占用的正机器人 ID 插入资料；机器人 ID 与用户 ID 属于不同身份空间，不创建用户密码、登录 Token 或群成员记录。以下仅是首次配置示例，ID 1 须确认在机器人表中未占用，不对已有资料自动 UPSERT：

```sql
SELECT id, code, display_name, status FROM im_bots;
-- 确认 code 和 ID 均未占用后，按实际选定值执行：
INSERT INTO im_bots (id, code, display_name, status)
VALUES (1, 'task-assistant', 'AI 助手', 1);
```

`.env` 填写 `IM_BOT_CERT_DIR` 和 `AGENT_BOT_CERT_DIR` 为两个独立私有目录，各含 `cert.pem`、`key.pem`、`ca.pem`；每个服务只挂入自己的私钥。IM 证书需服务端用途，DNS SAN 须匹配 `AGENT_IM_BOT_SERVER_NAME`（默认 `im.go-im.internal`）；Agent 客户端证书需客户端用途、受 IM 信任 CA 验证，且包含 `IM_BOT_AGENT_DNS_NAME` 的精确 SAN（默认 `agent.go-im.internal`）。两侧 CA 文件分别包含对端的可信签发根；不接受任意同 CA 客户端或通配符 Agent 身份。部署凭证由用户最终准备，测试临时证书不用于部署、不写入仓库。证书更新须重启相应进程，没有热更新。

非 Compose 启动 Agent 时，配置 `AGENT_IM_BOT_ADDR`、`AGENT_IM_BOT_SERVER_NAME`、`AGENT_IM_BOT_TLS_CERT_FILE`、`AGENT_IM_BOT_TLS_KEY_FILE`、`AGENT_IM_BOT_TLS_CA_FILE`。五项全空关闭回帖；部分配置、非法地址/身份或不可加载证书会拒绝启动，不降级为明文。连接在进程内复用，证书加载通过不等于已经成功握手，对端鉴别在实际 RPC 时完成；普通 `IM_RPC_ADDR` 仍用于权限和上下文读取。

只在最终联调前提满足时使用覆盖文件；先解析，按已有 Compose 项目和卷安排构建、重启，不在本轮执行：

```sh
docker compose --env-file .env --profile agent -f docker-compose.yaml -f docker-compose.bot.yaml config --quiet
```

覆盖文件只读挂载现有证书目录（`create_host_path: false`），默认等待 Kafka 健康并使用 `kafka:19092`；`IM_BOT_KAFKA_TOPIC` 必须与当前 Push 的聊天 Topic 一致。未准备证书时继续用基础 Compose。配置目前只做本地 YAML/字段测试，未用 Docker Compose 实际解析合并、构建或启动；真实证书、MySQL/Kafka/Redis及云端仍未验收。现有下面的用户 RPC 早期联调示例继续作为历史步骤参考，最终整体验收按最新阶段计划进行。

先确认云端是否已有这次本地改动：检查 `deploy/docker-compose.yaml` 中是否存在 `api-gateway`、`user-rpc`，以及 `deploy/api-gateway.yaml`、`deploy/user-rpc.yaml` 是否存在。若没有，先同步本地代码和配置模板；保留云端现有的 `.env`、`docker-config.local.yaml` 与 MySQL 数据卷，不用模板覆盖实际凭证。

在云服务器的 `deploy` 目录依次执行：

```sh
docker compose --env-file .env -f docker-compose.yaml config --quiet
docker compose --env-file .env -f docker-compose.yaml ps --all
```

第一条只验证配置，不启动容器；第二条确认旧 `im-api`、`mysql` 以及新增 `api-gateway`、`user-rpc` 的当前状态。确认仍使用原有 Compose 项目和数据库卷，且 `mysql`、`im-api` 正常运行后，构建并启动本次新增的两个服务：

```sh
docker compose --env-file .env -f docker-compose.yaml build user-rpc api-gateway
docker compose --env-file .env -f docker-compose.yaml up -d --no-deps user-rpc
docker compose --env-file .env -f docker-compose.yaml up -d --no-deps api-gateway
docker compose --env-file .env -f docker-compose.yaml ps --all user-rpc api-gateway mysql im-api
```

这里先构建，再用 `--no-deps` 启动，是为了避免这一步触发旧 MySQL 容器的重建。若新增容器已经运行，只在确实需要更新镜像时执行构建与启动命令。

随后使用自己的两个已有账号，在 API 客户端中逐一验证；不要把密码或 Token 贴到聊天：

1. `POST http://<服务器地址>:8080/api/v1/user/login`，JSON 请求体为 `{"username":"<用户名>","password":"<密码>"}`。旧 API 成功时返回 `code: 0` 和 `data.token`；只看到 HTTP 200 还不足以说明登录成功。
2. `GET http://<服务器地址>:8082/api/v1/user/info`，请求头为 `Authorization: Bearer <上一步的 Token>`。应返回 HTTP 200、`code: 0`，并且 `data.username`、`data.nickname` 对应刚登录的账号。两个账号应分别返回自己的资料。
3. 不带 Token 再请求一次，应返回 HTTP 401；添加 `?user_id=<其他用户 ID>` 应返回 HTTP 400。这样可以确认新入口不接受调用方指定查询对象。
4. 再通过新增的 `POST http://<服务器地址>:8082/api/v1/user/login` 登录，使用新 Token 请求第 2 步的本人查询；密码错误应返回 HTTP 401。这样验证新登录与本人查询之间的完整链路。
5. 使用新 `POST http://<服务器地址>:8082/api/v1/user/register` 注册一个测试账号，再用新登录和本人查询确认资料；相同用户名再次注册应返回 HTTP 409。测试账号及其数据应按届时约定管理，避免影响真实用户。

如果第 1 步失败，先检查旧 `im-api` 与 MySQL；如果第 2 步返回 401，核对 `.env` 的 `JWT_SECRET` 是否与 `docker-config.local.yaml` 的 `jwt.secret` 相同；如果返回 503 或 504，查看 `api-gateway` 和 `user-rpc` 的容器状态及日志。排查时只提供去除凭证后的错误信息。上述请求仍未在真实云端执行，完成后才能将真实数据库联调标记为通过。

## Agent 负责人解析字段（2026-10-03）

更新 Agent 前，已有库在草稿表 010/011/012 的基础上须执行一次 [016_agent_draft_assignee.sql](mysql/migrations/016_agent_draft_assignee.sql)；读取旧草稿也依赖这两列，不是仅新生成时才需要。新库初始化已包含。默认空字符串保留旧草稿的原负责人及创建结果，迁移不改任务、请求键或旧回帖。迁移未在真实数据库执行，最终同步时按实际结构与已执行记录核对。

User 服务须同时包含 `ResolveTeamMember`，原 `USER_RPC_ADDR` 复用，不新增端口或环境变量；旧 User 未实现该方法时，有称呼的生成会失败，不回退为无负责人。Agent 模型输出契约追加必填 `assignee_name`，本地替身已验证，方舟真实输出未验收。

本轮只贯通 Agent 的生成、解析状态持久化和读取 RPC。含称呼草稿暂不能通过旧确认接口创建，Gateway/页面的负责人审查与选择后续接入；不要据此认定完整负责人指派或阶段 6 已可上线。没有部署容器或同步云端，见[完整审查记录](../docs/agent-assignee-design.md#8-agent-提取解析与持久化审查2026-10-03)。

## Agent 草稿版本迁移（A50）

更新 Agent 前，已有数据库在 016 后须核对并执行一次 [017_agent_draft_revision.sql](mysql/migrations/017_agent_draft_revision.sql)，新库初始化已包含 revision。旧草稿默认版本 1，原负责人、任务键、任务结果和回帖不改变；读取/锁定旧草稿也依赖新列，仅更新 init.sql 不会改变已有数据卷。

此轮版本契约要求 Agent、Gateway、页面协调更新：旧数据可读，缺版本旧写入明确拒绝，不保留绕过版本的旧写入模式。版本机制是现有草稿表的增量更新，没有增加服务或后台调度。全量 Go、Node 80 项与 Linux Agent/Gateway 构建通过；脚本未在真实 MySQL 执行，容器与云端同步按用户约定留到最终部署。[完整审查](../docs/agent-assignee-design.md#10-草稿版本基础与现有写入接线审查2026-10-03)。

## Agent 自动截止时间依据迁移（2026-10-03）

已有数据库在017后须核对并执行一次[018_agent_draft_deadline.sql](mysql/migrations/018_agent_draft_deadline.sql)，再更新Agent；读取/锁定旧草稿也依赖新增九列。新库init.sql已包含，旧数据卷不会因改init.sql自动升级。空/0默认保留旧草稿、原deadline、版本、Task键/结果及回帖，不重新解释历史表达。本批只准备脚本，没有执行真实迁移；最终同步时核对数据库实际结构与迁移记录。

Agent、Gateway、页面需协调升级：模型输出新增三个必填时间原文字段；新deadline依据草稿确认要求当前处理状态和时间一起审查，旧页面不能盲确认，旧legacy草稿保留旧规则。模糊表达须本人补完整时间或明确保存不设置，不能因due=0视为已处理。仍用原RPC地址、数据库和模型配置，不新增端口、环境变量或服务。真实方舟输出、数据库事务及浏览器/容器联调待最终统一验收，见[共同契约](../docs/deadline-auto-contract.md)。

## 多项草稿保存/读取迁移（2026-10-04）

更新本批 Agent 前，已有库须在 018 后核对并执行一次 [019_agent_draft_collection.sql](mysql/migrations/019_agent_draft_collection.sql)。新库初始化包含 run 的 `draft_mode/item_count` 和 draft 的独立 `status`；已有数据卷不会因更新 init.sql 自动升级。默认 single/1 与空项状态保留旧 run 的权威状态、内容、版本、Task 键/结果和回帖；新集合明确保存 collection 模式、实际项数和每项 waiting 状态。

本批没有新服务、端口、环境变量、依赖或模型账号，沿原 Agent/Gateway 配置接入新生成和读取接口。旧单项入口拒绝集合，当前页面仍走原单项入口；多项页面、逐项确认/跳过/回帖后续实现。迁移文件只准备，真实 MySQL、模型、容器和云同步均未执行，最终部署时再协调版本与数据库结构。[完整审查](../docs/multi-draft-storage-review.md)。
