# 逐项回帖 IM 接收端审查

2026-10-04。沿用户已确认 A44/A46/A47/A55 和[共同契约](multi-reply-im-contract.md)。上轮本人跳过完整9a02643已快进合本地main；本轮共同196c97d统一专用RPC/generated、固定项消息 ID 和020/init/schema检查，公共model/IM/Agent/Gateway回归通过。三个独立worktree处理接收、存储发布、TLS组合，root审查提交b8a8b40（2文件）、6b87cfc（4文件）、ac26318（1文件），按顺序无冲突整合，业务代码c3f3411已通过下列验证。当前集成分支codex/multi-reply-im-integration供审查，main保持9a02643。

## 七步及调用链

1. 共同协议：新增专用 PostTaskCreatedCardItem 和显式项身份，统一 helper，第0项保留原ID；020只增加默认0列，014旧迁移保留。解决旧服务把所有项默认为0和迁移破坏旧依据的问题。
2. 接收：新/旧方法共用证书、Token、当前团队群资格和启用机器人流程；旧方法恒0，新方法不能缺项。解决可信服务证书被误当成永久用户授权的问题。
3. 入口保护：拒绝越界项/非法卡片/错项返回；显式第0项与旧接口兼容，accepted重放仍重新授权。
4. 持久去重：固定run/index/消息ID/范围/正文/时间，独立项存独立记录；同项换内容或范围拒绝，原时间/受理结果保留。
5. 同步发布：提交依据后事务外同步Kafka，保存受理结果；失败重试同一消息，异常存储结果不能发布，其他项不被覆盖。
6. 本机TLS组合：生产Agent客户端→生产IM专用mTLS监听→真实授权/发送存储适配；SQL/User为替身。Kafka发布故障由生产publisher＋writer替身单独覆盖，未连接真实Kafka。
7. 集中审查：核对允许文件、顺序整合、定向/全量测试及Linux编译，记录全部实际文件和未验证部分。

`可信 Agent＋原本人 Token＋已保存任务卡片 → IMBot.PostTaskCreatedCardItem(run,index) → TLS/当前团队群资格/机器人资料 → IM 固定项发送记录 → 同步 Kafka → 持久 accepted`。

本批测试可直接由生产客户端提交卡片；业务中的 Agent 集合确认尚不自动调用新方法，不能把测试请求当成已接入逐项确认/回帖编排。

## 验证与限制

共同准备：`go test ./internal/model ./rpc/im ./rpc/agent ./api -count=1` 通过。整合c3f3411定向 `go test ./rpc/im ./internal/model ./rpc/agent -count=1` 通过；`go test ./... -count=1` 全量通过，含新三个实际TCP/mTLS组合、完整原单项和多项编辑/确认/跳过回归。`node --test examples/chat.test.cjs` 140/140通过，本轮不改页面；`CGO_ENABLED=0 GOOS=linux go build` 的IM、Agent、Gateway通过，未启动容器。协议正常再生成；Go格式与diff检查通过，整合后的CRLF规范为gofmt格式，Git规范化后没有新增业务差异。

入口新增6个、存储/发布6个、TLS组合3个测试函数，共同ID/schema各1个；数量不代表功能完成率。SQL替身验证固定项列、旧0/新0、同项换范围/正文/序号/消息ID拒绝、原时间受理保留；发布器＋实际MySQL适配/SQL替身＋Kafka writer替身覆盖ACK不明、受理写失败及已保存但响应丢失，同项重试保持原事件字节且其他项不变。异常记录在accepted快捷返回前拦截，不伪受理或发布。实际生产TLS运行时只返回既有accepted SQL记录，不触发其真实Kafka writer；新方法对旧bot业务替身返回Unimplemented，不触发旧方法，无自动降级。

本批只是逐项回帖的接收基础。Agent逐项回帖意图表、创建成功后的同步编排、本人显式重试RPC/HTTP、多项页面与汇总仍后续。IM信任经证书鉴别的Agent结果，不查询Task或Agent数据库。accepted只表示Kafka ACK和IM受理记录保存；响应不明或保存失败可能重复同一事件，客户端按msg_id去重，不表示历史已落库或成员已送达。

020和真实MySQL升级/锁/唯一约束、真实Kafka/Push链、部署证书、浏览器/容器、模型及云端都未验收。本批未执行真实迁移/模型/中间件/部署/push，阶段6不标全部完成。改动保持独立集成分支供用户审查，三个worktree保留。

## 全部实际修改文件

相对于本地main9a02643，共 **23个实际修改文件**；业务改动集中在入口、存储、发布和一个公共ID函数，其余为协议生成、必要测试、迁移和审查/部署文档。

| 文件 | 实际作用 |
| --- | --- |
| [rpc/im/bot.proto](../rpc/im/bot.proto) | 专用项发送方法，显式optional index和原卡片 |
| [rpc/im/pb/bot.pb.go](../rpc/im/pb/bot.pb.go) | 正常生成项请求类型，旧字段不变 |
| [rpc/im/pb/bot_grpc.pb.go](../rpc/im/pb/bot_grpc.pb.go) | 正常生成新方法客户端/服务桩 |
| [internal/model/bot_task_msg_id.go](../internal/model/bot_task_msg_id.go) | 统一稳定项消息ID，保留第0项旧键 |
| [internal/model/bot_task_msg_id_test.go](../internal/model/bot_task_msg_id_test.go) | 最大run/索引/长度/不冲突/旧兼容 |
| [rpc/im/bot_reply.go](../rpc/im/bot_reply.go) | 新旧共享授权、强制索引和返回身份保护 |
| [rpc/im/bot_item_reply_test.go](../rpc/im/bot_item_reply_test.go) | 精确TLS/原Token/当前资格/非法请求/旧0回归 |
| [rpc/im/bot_send_store.go](../rpc/im/bot_send_store.go) | 固定项记录、消息ID/完整意图比较 |
| [rpc/im/bot_send_store_test.go](../rpc/im/bot_send_store_test.go) | SQL fixture扩项列，保留原单项断言 |
| [rpc/im/bot_publisher.go](../rpc/im/bot_publisher.go) | accepted重放前核对存储记录身份和卡片 |
| [rpc/im/bot_item_send_test.go](../rpc/im/bot_item_send_test.go) | 独立项/冲突/固定事件/SQL与Kafka失败恢复 |
| [rpc/im/bot_item_flow_test.go](../rpc/im/bot_item_flow_test.go) | 实际生产客户端与IM监听mTLS/旧方法/撤权/隔离 |
| [rpc/im/bot_item_schema_test.go](../rpc/im/bot_item_schema_test.go) | 014加020与新初始化表完整一致性检查 |
| [deploy/mysql/init.sql](../deploy/mysql/init.sql) | 新库IM发送表默认项0 |
| [deploy/mysql/migrations/020_im_bot_send_items.sql](../deploy/mysql/migrations/020_im_bot_send_items.sql) | 已有库只追加项列，保留原记录 |
| [rpc/im/README.md](../rpc/im/README.md) | 项方法、权限、兼容及Agent尚未接线边界 |
| [deploy/README.md](../deploy/README.md) | 020前置顺序和旧服务不降级 |
| [docs/multi-reply-im-contract.md](../docs/multi-reply-im-contract.md) | 共同接口/身份/数据/分工与七步 |
| [docs/multi-reply-im-review.md](../docs/multi-reply-im-review.md) | 本文：全部实际文件、调用链和验证限制 |
| [docs/architecture-decisions.md](../docs/architecture-decisions.md) | A55实施方案、备选、理由/代价及验证关联 |
| [docs/project-plan.md](../docs/project-plan.md) | 实际本地成果、未验收和下一步 |
| [docs/worktree-collaboration-plan.md](../docs/worktree-collaboration-plan.md) | 三个独立角色、分支、允许文件和集成记录 |
| [docs/stage6-acceptance.md](../docs/stage6-acceptance.md) | 020/项回帖真实验收要求，阶段不误标完成 |
