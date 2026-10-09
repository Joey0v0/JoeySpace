# F3 第一批共享契约（2026-10-09）

用户已确认 [F09—F11](frontend-f3-chat-design.md)，本文件固定首批实现字段。F10/F11 的新读写协议留第二批共同提交；首批只复用现有消息/未读 RPC 和 HTTP 路由。

## WebSocket 连接票据

- WS 服务公开 `POST /ws-ticket`，请求 `Authorization: Bearer <JWT>`，无请求体；成功为 HTTP 200、`{"code":0,"msg":"ok","data":{"ticket":"<base64url-random>","expires_in_seconds":30}}`。JWT 无效/缺失 401；Redis 不可用 503。响应 `Cache-Control: no-store`，不得在日志记录 Token 或票据。签发仅确认 JWT 有效，后续群发送仍按原 IM 资格检查。
- 票据用 32 字节随机数，服务端仅以票据 SHA-256 摘要为 Redis 键，值包含本人 ID 与原 Token，TTL 30 秒。`GETDEL` 原子消费；未知、过期、重用一律 401，Redis 故障 503。消费后再次解析原 Token 并比对本人 ID 与过期时间；票据不可用于 HTTP API。原 Token 仅存在短期服务端 Redis 记录，客户端连接 URL 只有票据。
- `GET /ws?ticket=...` 与旧 `GET /ws?token=...` 互斥；同时或都缺失返回 400/401，旧 Token 路径只为演示/旧客户端保留。无论哪条路径，升级前检查浏览器 Origin：与请求 Host 同源可通过；跨源须精确匹配 `WS_ALLOWED_ORIGINS` 的逗号分隔 HTTP(S) origin。无 Origin 的非浏览器客户端沿旧行为可用；配置不能用 `*`。非法 Origin 403，且不消费票据。
- Vue 开发代理 `/ws` 和 `/ws-ticket` 到本机 WS 8081；正式站点需在同一站点反代这两条路径，F6 实际部署验收。使用旧 `/demo/chat`（Gateway 8082 → WS 8081）时须把页面 Origin 显式加入 `WS_ALLOWED_ORIGINS`。代理/access log 不输出查询串；旧 Token 入口后续另行废弃。

## 现有聊天 HTTP 与 WS

- 群历史 `GET /api/v1/teams/:team_id/groups/:group_id/messages?before_message_id=&limit=`，私聊历史 `GET /api/v1/direct/:peer_id/messages?...`。持久消息 `id` 和游标是十进制字符串；`msg_id` 是发送去重键，不代替持久 ID。
- 群/私聊未读分别用现有 `/unread`；显式确认只走现有 `/read`，请求 `message_ids` 为当前已加载且符合服务端规则的十进制字符串，单次最多 100 条。读取历史/实时消息/离线 ACK 不隐式调用 `/read`。
- 上行 `{"type":"chat","data":{"msg_id":"<stable-id>","to_id":"<decimal>","chat_type":1|2,"content_type":1,"content":"..."}}`；服务端 `ack.data.msg_id` 只表示 Kafka 受理。错误帧当前不带 `msg_id`，首批同一连接只允许一个发送中消息。下行 `chat.data.id` 是持久字符串 ID，可用于分页/显式已读；重连后重查历史和未读。
- 离线 `GET /api/v1/message/offline`、`POST /api/v1/message/offline/ack` 只确认投递处理，与已读分离。该 GET 当前无分页，客户端不得将有限载入误称完整历史；按 `msg_id` 去重后只对已安全处理的消息 ACK。
