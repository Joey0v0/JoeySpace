# 阶段7：整体体验与最终验收清单

更新2026-10-06。依据[阶段路线](project-plan.md)、[通知既定方案](stage7-notification-realtime-design.md)及[阶段6真实服务准备](stage6-runtime-acceptance.md)。本页是收尾清单，不代表阶段7或整个项目已完成。用户决定先完成本地开发与测试，最后统一同步云端；本批不运行迁移、部署或真实模型。

## 1. 完成条件与当前缺口

| 路线要求 | 当前可核对能力 | 仍需完成或确认 |
| --- | --- | --- |
| 任务变更通知 | Task事务保存通知及Outbox，Kafka发布，独立Push消费，专用mTLS WS提示；本人分页/逐条已读、原生提示/重连查询已有本地验证 | 本批补分段组合恢复验证；真实MySQL/Kafka/Redis/User、浏览器与部署链待验收 |
| 未读消息 | A73本人团队群逐消息凭据/Get/Mark/Gateway/原生显式按钮；显式历史重遍历、提交后丢响应/撤权恢复已有本地组合验证；本人普通排除、机器人计入。A74旧出口也经IM当前资格，离线ACK独立 | 030未执行/真实DB未验收；A16退出清理/Push资格、单聊目标仍需后续核对。[原实现](stage7-team-group-unread-contract.md#本批实现与审查)、[本批恢复边界](stage7-group-unread-recovery-contract.md#本批实现与审查)，不将ACK或ID游标当已读 |
| Agent执行记录 | 持久Inbox、运行/草稿、逐项确认/跳过/结果/回帖；原指令查询状态和本人读取草稿 | 已核对记录和A64本人状态边界，缺安全失败阶段/关联日志；先沿现有内部日志补齐，不扩本人错误/预算接口。真实模型/页面待实测，没有通用运行列表或完整工具审计 |
| 部署与排查 | Compose基础、可选覆盖、迁移与启动文档；四个业务RPC启动已抑制go-zero统计请求正文，并通过真实框架本机对照测试 | 完整覆盖组合、私有配置、模型预算、证书挂载、镜像启动和回退待实际核对。安全关联日志仍有缺口，正文修复不等于全部日志脱敏或全链追踪 |
| 核心演示 | 用户/团队/群聊/任务/Agent接口和页面代码逐步实现 | 实际完成双账号注册登录、协作聊天、@AI草稿、逐项确认/回帖、任务变化提醒及重连查询全过程，并记录证据 |

后续实现只围绕上述已确定目标。新增会话已读规则、运行查询边界或观测中间件属于关键选择，先讨论并记入架构决策；本页不授权新增框架或功能，也不把缺项静默移出范围。

2026-10-06 A75/A76/A77全部A已确认：[资格基础](stage7-team-membership-foundation-contract.md)已准备031/032、User活动状态过滤/正版本、IM关闭事务组件；[写入保护](stage7-team-group-write-guard-contract.md)把正版本/关闭记录接入自行入群与建群群主写入，旧IM原始SQL中的`groups`引用已修。升级需先核对001/执行031再升级User，执行032再升级IM，不能让新版IM依赖旧User缺版本响应。退出/恢复/清理、Push核权仍未接线；普通读取接线见下一段，后台读取仍后续。真实MySQL语法/锁竞争未验收。MySQL8将GROUPS列为保留字，[官方关键字说明](https://dev.mysql.com/doc/refman/8.0/en/keywords.html)；sqlmock通过不能替代实库执行。

2026-10-06 [普通群读取保护](stage7-team-group-read-guard-contract.md#本批实现与审查)已在User本人/正版本核权后，重新核对当前群成员和032关闭版本；历史、未读、来源、机器人和旧离线拉取继承普通CheckGroupMember。旧群不走User/关闭行，User拒绝后不读取群正文。随后[后台触发上下文保护](stage7-trigger-generation-guard-contract.md#本批实现与审查)使专用mTLS User资格RPC回显真实正generation；IM按持久触发范围在历史前后各复核User同版本及IM当前群成员/关闭记录，旧User零版本固定失败。[负责人解析末次保护](stage7-trigger-resolver-final-guard-contract.md#本批实现与审查)又使User候选查询前后版本一致，IM候选调用前后版本一致并最终查群关闭。User/IM定向、双mTLS组合及全仓Go通过，SQL数据库仍为替身。真实MySQL/031/032/并发、退出/清理/Push仍未完成，不能把本地读取保护说成完整团队退出链路。

2026-10-06 [033 User退出操作表](stage7-team-leave-operation-schema.md)已准备，与新库init的DDL经换行标准化后静态一致；迁移不创建操作、不撤权、不清理群。后续启用退出业务前须在已有User库核对001/031并执行033一次。真实MySQL、事务竞争和退出→清理→重试仍未验收。

2026-10-06 [User退出意图事务](stage7-team-leave-intent-contract.md#本批实现与审查)在包内把成员变为leaving与插入033操作放在同一短事务，支持同键回显，真实拥有者拒绝退出；User包与全仓Go测试通过。没有公开的本人Token入口，没有IM清理或Push核权；033、真实MySQL并发/重启仍未验收，不算完整退出功能。

## 2. 本地验证证据与边界

| 验证段 | 已有证据 | 不代表什么 |
| --- | --- | --- |
| Task状态→通知/Outbox→发布结果不确定后重建 | [Task发布组合](../rpc/task/notification_publication_flow_test.go)，使用生产状态/store/publisher及SQL/Kafka替身 | 未验证真实MySQL事务、实际Kafka ACK或进程崩溃 |
| Push消费→生产TLS客户端→WS处理器 | [真实TLS离线组合](../internal/push/task_notification_flow_test.go)，临时证书与本机HTTPS | 原有此段为offline，在线帧由本批下方另一组合覆盖，不能合称完整端到端 |
| WS权限/队列、坏事件/暂时故障/offset确认 | [WS测试](../internal/ws/task_notification_test.go)、[消费者测试](../internal/push/task_notification_consumer_test.go) | 队列接受不是浏览器收到；明确offline/denied确认不提供离线提示补发 |
| Gateway HTTP→TCP gRPC | [本人列表](../api/task_notifications_transport_test.go)、[逐条已读](../api/task_notification_read_test.go) | Task服务为替身，不是HTTP贯通实际Task数据库 |
| 页面提示→本人刷新/重连查询→已读操作 | [页面审查](stage7-notification-realtime-page-contract.md#本批实现与审查)，303项Node及API回归通过 | VM/DOM/HTTP/WS为替身，不是实际浏览器或生产链 |
| 本批相邻组件组合 | [共同契约与实际结果](stage7-notification-flow-contract.md#本批实现与审查)：生产发布输出进入consumer、真实TLS/WS最小帧、生产TCP RPC已读丢响应及撤权恢复；全仓Go通过 | SQL/Kafka/User等仍替身，分段组合不能合称真实全链路验收 |
| RPC统计请求正文抑制 | [框架对照与审查](stage7-experience-gap-design.md#本批实现与审查)：四个服务的方法策略，普通/慢调用四类对照及全仓Go通过 | 模拟敏感字段、未实现业务处理器；不是全部日志审计、实际镜像或部署验收 |
| A74 IM离线读取核权 | [权限测试](../rpc/im/offline_access_test.go)：混合聊天/机器人、同群复用、撤权/恢复、核权故障无部分结果；IM/API定向及全仓Go通过 | SQL/User替身，旧Gin本地收口证据见下一行；不保证撤权和返回原子一致，不验证真实离队清理 |
| A22/A74旧Gin离线出口 | [实际HTTP→TCP生产IM组合](../rpc/im/legacy_offline_flow_test.go)：原认证/Token/字符串ID/卡片、团队资格拒绝/恢复、故障无部分正文、本人ACK；Handler/工厂定向及全仓Go通过 | SQL/User为替身，没有真实MySQL/云端或旧API完整信号关闭验收；ACK新32KiB/单JSON限制，Compose只静态解析 |
| A73本人团队群未读 | [IM](../rpc/im/team_group_unread_test.go)12函数、[Gateway](../api/team_group_unread_test.go)7函数、模块16项及真实HTML脚本组合；全仓Go/325项Node通过。030/init静态一致、三服务包运行-h通过 | SQL/User/DOM/fetch替身，部分HTTP/TCP本机实际；首次时间/迟到ID是SQL结构与回放证据，非真实MySQL时序；030/浏览器/云未验收，历史重遍历后续 |
| A73历史重遍历与未读恢复 | [页面组合](../examples/team-group-unread-recovery.test.cjs)24项执行实际内联与未读脚本，全部353项Node通过；[生产IM TCP](../rpc/im/team_group_unread_recovery_test.go)两项定向通过，命中成功提交后失败回包/提交后撤权再恢复；[集中结果](stage7-group-unread-recovery-contract.md#本批实现与审查) | DOM/HTTP/SQL/User为替身；严格SQL不写或更新read_at是结构证据，计数由替身模拟，不证明真实MySQL首次时间、进程崩溃持久性或实际退出清理；030和实际浏览器仍待验收 |
| A75后台触发上下文代际核权 | [专用mTLS与代际场景](../rpc/im/trigger_context_generation_test.go)、[User真实回显测试](../rpc/user/trigger_tls_flow_test.go)及[集中结果](stage7-trigger-generation-guard-contract.md#本批实现与审查)；旧User零版本、读中换代/撤权/关闭无部分正文 | SQL/User数据库均替身，本机mTLS不代表云端证书/真实MySQL/031/032/跨服务原子性；负责人解析末次核权与User解析器前后版本比较仍待 |
| A75负责人解析前后代际核权 | [User候选查询版本测试](../rpc/user/trigger_member_generation_test.go)、[实际双mTLS组合](../rpc/im/trigger_resolver_generation_flow_test.go)和[集中结果](stage7-trigger-resolver-final-guard-contract.md#本批实现与审查)；成功、空候选、查询中换代、解析后撤权/关闭均已本地验证 | 数据库仍用SQL替身；本机TLS不证明真实031/032、跨服务原子性、退出清理或云端证书配置 |

## 3. 最终启动前核对（全部待执行）

1. 记录验收代码提交、Compose项目名、已有数据卷与备份位置；核对全部已用迁移，通知链需027、028、029，IM未读需030，Agent另需先前草稿/机器人及022—026等迁移。按真实表结构和迁移记录确定顺序，不盲目重跑，不用更新init.sql代替旧库升级。
2. 保留现有私有.env/docker-config.local.yaml凭证；协调User/IM/Task/Agent节点ID和RPC地址、JWT及数据库连接。共用MySQL实例不等于已完成逻辑库/账号隔离；该边界仍需项目收尾审查。
3. Task发布开关与Topic必须和Push YAML的独立Topic一致；Push/WS YAML角色分别显式enabled。检查聊天、Agent触发、任务通知Topic/group相互隔离。新组读取最早保留事件，旧组沿已提交offset，029不回填旧通知。
4. 准备独立通知Push/WS证书和精确SAN用途，检查容器只读路径、有效期、信任CA及`im-ws:9091`→`https://im-ws:9443`映射。Agent bot/trigger另有证书配置，不共享私钥，不部署测试临时证书。
5. 用户确定方舟接入点和预算后，在不入库的私有配置填ARK_MODEL_ID/ARK_API_KEY。当前生产Agent启动需要模型配置，本地测试假模型不是可启动的生产离线模式。
6. 在最终验收环境解析完整覆盖，再构建/启动；不删除数据卷。当前机器缺Docker，以下命令**未执行**，单纯解析成功也不代表服务可运行：

   ```sh
   docker compose --env-file .env --profile agent -f docker-compose.yaml -f docker-compose.bot.yaml -f docker-compose.trigger.yaml -f docker-compose.notifications.yaml config --quiet
   ```

完整证书/配置和迁移说明见[部署文档](../deploy/README.md)与[阶段6启动前提](stage6-runtime-acceptance.md#1-启动前核对未执行)。如果某可选能力本次关闭，明确记录并保留为未验收，不能删减最终核心演示要求。

## 4. 正常通知演示（全部待验收）

使用隔离团队T，创建者A和负责人B，另外准备非成员C；消息、任务和通知使用真实保存的ID。通知与聊天接收人规则不同，不以群资格代替当前团队资格。

| 操作 | 必须观察的事实 |
| --- | --- |
| A/B经Gateway登录，完成团队群聊天与人工任务创建，B连接WS并填有效T | 注册/登录/团队/群/任务链正常；通知首次GET可完成，普通聊天不受提醒开关影响 |
| 有权用户改变任务状态 | task_operations、接收人通知及同ID Outbox一起保存；创建者和当前负责人去重，各自只有一条，重复设置同状态不新增 |
| 开启通知发布及Push/WS后等待提醒 | Outbox published仅表示Kafka ACK，consumer提交的是原事件offset，B页面只显示“请刷新通知”；最小帧不含Token、正文或接收人，尚未自动已读 |
| B手动刷新并点击单项标已读，再重复点击/刷新 | 最新通知变化正确，ID用字符串；首次read_at稳定，GET不改变已读，重复确认不改第一次时间 |
| A和B分别读取自己的通知；C尝试同T查询或已读 | 各读本人，不能自报接收人；C被拒绝；不同团队/接收人记录不得暴露 |
| B断开时产生新通知，然后手动重连 | 可以不补在线提示，但本人当前授权GET恢复持久通知；重连不会自动标已读或创建任务 |
| 从原群指令生成多项草稿，逐项确认/跳过并回帖，再改变生成任务状态 | 群消息→Inbox→运行/草稿→Task→机器人卡片→任务通知完整串联；每项固定任务与消息身份，参照阶段6清单记录 |

### 团队群未读与重新遍历（真实环境仍待验收）

1. A加载团队群历史并逐页显式确认，检查本人普通消息不计、机器人计入；读取历史、刷新计数、WS收到及离线ACK均不自动写阅读凭据。
2. 已完成一轮分页后，模拟较小ID消息延后入库。计数仍增加；“刷新最新群消息”保留旧位置，“从最新消息重新遍历”成功后可继续向前翻找到该消息。只确认实际已加载的那一页，不能通过最大ID越过迟到消息。
3. 在重遍历期间制造临时失败、非法响应或切换账号/群，核对旧位置没有被失败或旧回包替换。成功重新遍历才更新位置，原消息显示按msg_id去重，但已显示过的有效历史页仍可明确确认。
4. 在Mark数据库提交后阻断响应，先查询恢复计数，再本人重试原消息集合；检查个人主键没有新增重复凭据，真实数据库read_at首次时间未变。进程重启后重复核对，并记录实际故障注入窗口，不能只停服务冒充提交后丢响应。
5. 当前团队或群资格撤销时读取/确认均拒绝，原已读记录保留；重新获得团队及群资格后再次核对。真实退出清理与Push资格链仍待后续实现，不用测试替身冒充该业务入口已完成。

本地组合证据与实际范围记录于[本批契约](stage7-group-unread-recovery-contract.md)，真实迁移030/MySQL时序、浏览器、完整服务与云端链保持未验收。

## 5. 故障与恢复（仅隔离环境，全部待验收）

| 故障 | 期望行为与证据 |
| --- | --- |
| Task发布后未保存published，或ACK不确定 | 重启后沿同通知ID/Key/内容重发；允许重复，持久通知不重复创建；页面有界合并，不能要求无限期全局去重 |
| Push临时在线查询/User/HTTPS故障或WS队列满 | 不提前提交当前offset，恢复后再处理原事件；队列满不注销聊天连接，不伪称queued |
| 提醒投递成功后commit失败 | 同运行实例只重试commit，不重新投递；若进程终止，Kafka可能重放，不能宣称恰好一次 |
| 明确离线、连接Token失效或已离队 | 确认消费但不补在线提示；通知仍留Task，再读取按当前团队资格。客户端401/403清旧资料，不能据此宣称后端撤权瞬间自动清屏 |
| 非法/不支持版本/错误Key的通知事件 | 只停止独立提醒消费，保留未确认offset，聊天继续；定位并修复后新reader重启，不跳过或重置现有组掩盖问题 |
| 已读已提交但响应丢失 | 本人重读能观察原时间，再次确认不产生新更新时间；失败不能直接判定数据库未写入 |
| 账号/团队切换、旧socket迟到、刷新期间新提醒 | 旧范围结果及回调不能覆盖当前；有效新提醒仍显示，不自动已读 |
| 证书错误、映射错误、端口占用、停止启动流程 | 错误可定位且不降级明文；只在隔离环境验证退出/关闭顺序，不把HTTP Shutdown说成原聊天全部排空 |

故障证据必须记录注入点、时间与实际状态变化。停服务不能冒充“提交成功但响应丢失”，平滑重启不能冒充崩溃；未真实命中窗口的场景保持未验收。

## 6. 最终结果记录模板

每项保存代码提交、迁移版本、脱敏配置差异、测试账号/团队范围、脱敏HTTP结果、关联message_id/run_id/task_id/notification_id、Outbox状态、Inbox状态及KafkaTopic/Partition/Offset、日志阶段和恢复动作。不要记录Token、DSN密码、私钥或完整私有聊天正文。

结论填写“通过／失败／未执行”，失败列对应文件/问题与修复后复验结果。全部核心演示和必要故障项有证据后，才更新项目阶段完成状态；本地测试数量不代替真实业务闭环。
