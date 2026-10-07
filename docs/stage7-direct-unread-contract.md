# 阶段 7：单聊逐消息未读契约

2026-10-07，用户已选择沿用团队群的逐消息已读口径；选型见[架构记录 A78](architecture-decisions.md)。034 迁移、IM 内部 RPC、Gateway HTTP 入口和原生演示页面均已准备。

IM 拥有 `messages` 和个人阅读记录。本人通过现有登录 Token 确定 `user_id`，指定对方 `peer_id`；单聊历史仅可读取 `chat_type=1` 且两端恰为本人和对方的消息。未读统计只计 `from_id=peer_id`、`to_id=user_id` 的消息，不计本人发送的消息。首次建立记录时，旧单聊历史仍按未读计算；列表读取、WebSocket 收到消息及离线 ACK 均不自动标记已读。

本人显式提交已加载的具体消息 ID，IM 在事务里逐个验证它们均为指定对方发给本人的单聊消息，任一无效则整批拒绝；相同 ID 重试使用主键冲突 no-op，不改第一次 `read_at`。服务端最多接受 100 个 ID，客户端不传 `user_id`，不使用最大消息 ID 水位。IM 新增 `ListDirectMessages`、`GetDirectUnread`、`MarkDirectMessagesRead`；历史以 ID 倒序分页，默认 20 条、最多 100 条。本人不能以自己为对方建立本接口的单聊范围。提交后计数查询失败时，阅读凭据可能已保存，客户端需先查询再用同一批 ID 重试。

Gateway 提供 `GET /api/v1/direct/:peer_id/messages?before_message_id=...&limit=...`、`GET /api/v1/direct/:peer_id/unread` 和 `POST /api/v1/direct/:peer_id/read`。POST 正文为 `{"message_ids":["123"]}`；路径、游标、消息 ID 与返回的大整数均为十进制字符串。Gateway 只转发登录 Token，由 IM 从 Token 确定本人；不接受客户端给出的 `user_id`。请求和回包不合法时拒绝，IM 错误只映射为不含内部详情的 HTTP 状态。

演示页在单聊类型 1 下提供最新页、从最新重新遍历、向前分页、未读数及“只确认本次收到的消息”操作。首次加载建立翻页游标；之后刷新最新消息保留原翻页位置并去除页面内重复；重新遍历仅在成功后换游标。页面切换 Token、接收人或聊天类型时清空历史/待确认消息，旧异步回包不能恢复旧范围；401/403 清空并阻止当前范围继续操作。失败的显式确认保留同一批 ID，提示先查询再重试；从不因历史、实时帧或离线 ACK 自动标记。

034 只新增 `im_direct_message_reads(user_id,peer_id,message_id)`，不回填、不删除离线记录或群已读记录。上线既有库需按顺序执行 034；全新库使用 `deploy/mysql/init.sql`。IM 定向测试验证双向历史范围、倒序游标、重复确认、混入错误消息整批回滚和 Token 身份；Gateway 定向测试验证大 ID、转发、输入和错误边界。页面模块及既有聊天/群未读共 193 项 Node 定向测试通过，Gateway 嵌入页面/脚本 Go 定向测试通过。此前全仓 Go 测试最终通过；首次运行时无关的 WS 端口释放测试偶发端口占用，单独复查和全仓第二次运行均通过。SQL 仍为替身，034 尚未连接真实 MySQL，也未验证大表查询计划；真实浏览器体验未验证，阶段 7 仍未完成。
