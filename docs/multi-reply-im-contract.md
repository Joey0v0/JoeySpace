# 逐项回帖：IM 接收端共同契约

2026-10-04。沿用户已确认 A37/A44/A46/A47/A55；本批只补 IM 接收、持久去重和真实本机 mTLS 验证。Agent 逐项意图持久化、确认后的回帖编排、Gateway 重试和页面仍待下一批，当前集合确认不能使用旧 run 级回帖发送其他项。

## 协议和兼容

新增专用 `IMBot.PostTaskCreatedCardItem`，请求 `run_id`、**显式 optional item_index（0..4）**、`team_id`、`group_id`、原 version-1 卡片正文，返回既有消息 ID/accepted。原 `PostTaskCreatedCard` 恒为 index 0，缺省项只允许旧入口；新入口缺项拒绝。旧 IM 对新方法返回 Unimplemented，调用者不得回退旧方法，避免旧服务忽略项字段而错误去重为第 0 项。

模型公共 `BotTaskItemMsgID`：正 int64 run，index 0 保持 `bot-task:<run>`，index 1..4 为 `bot-task:<run>:<index>`，最大 ID 小于 64 字节，不用 Task ID 替代 run，不增加汇总卡格式。旧消息内容、时间和已受理结果不变；旧/新 index 0 指向同一持久记录，换正文拒绝。

## 权限、存储和同步发布

两个方法只在原专用 TLS 1.3/mTLS 监听注册，必须精确 Agent SAN、原本人 Token、当前团队群资格和启用的 IM 机器人资料，**已受理重放也先重新授权**。不提供调用者填写用户或机器人 ID 的字段，不在普通 IM 监听注册，不改变业务服务边界。

`botSendIntent`/`botSendRecord` 加 `ItemIndex int32`；IM 自有 `im_bot_sends.item_index INT NOT NULL DEFAULT 0`，MsgID 主键继续是唯一发送键；准备记录前用公共 helper 验证项身份，重复键读取必须核对 MsgID、run/index、机器人、发起人、团队、群和原正文。重复记录的原时间/受理状态不得改写，非法存储结果不能发布/宣称受理。020 增量迁移只加默认 0 列，014 历史迁移保留，新初始化一致；升级 IM 程序前先迁移，真实迁移本批不执行。

先提交固定记录，再事务外同步 Kafka，确认后保存 accepted；并发、Kafka 结果不明或保存受理失败仍可重复投递**同一字节事件**，客户端按 msg_id 去重。accepted 只表示 Kafka ACK 和 IM 受理记录保存，不表示 Push 已落库或群成员送达。逐项受理互不覆盖，无后台自动投递、跨服务事务或 Redis TTL 替代。

## 分工和检查

主 agent 负责此契约、协议/generated、公共 ID helper/test、020/init、共同文档、提交整合和集中验证。执行 agent 不运行 Go 测试、不自行 commit/merge/push、不改协议/迁移/依赖/共同文档。

1. 接收入口：`.worktrees/assignee-backend`，仅 `rpc/im/bot_reply.go`、`bot_reply_test.go`、新 `bot_item_reply_test.go`；新方法和旧入口共享权限流程，显式项/卡片/返回值保护。
2. 持久发布：`.worktrees/assignee-gateway`，仅 `rpc/im/bot_send_store.go`、`bot_send_store_test.go`、`bot_publisher.go`、`bot_publisher_test.go`、新 `bot_item_send_test.go`；项记录/冲突/同步重试及旧兼容。
3. TLS 组合：`.worktrees/assignee-ui`，仅新 `rpc/im/bot_item_flow_test.go`；复用 production IM runtime 和 Agent NewBotReplyClient，SQL/User/Kafka 替身，旧 0/新 1/4、重放/冲突/权限撤销与监听隔离，待整合后集中跑。

整体七步：共同准备 1、接收 2、持久发布 2、TLS 组合 1、审查验证 1。每名 agent 可读其他源码理解，但修改不越界；依次接收→存储→组合整合。明确时间/身份规则已选，不新增架构或技术选择。
