# 小步迁移：通过用户 RPC 登录

本步新增 `POST http://127.0.0.1:8082/api/v1/user/login`，请求体与旧登录一致：

```json
{"username":"alice","password":"自己的密码"}
```

调用链：客户端 → go-zero API → 用户 RPC → 现有 MySQL `users` 表。API 只校验 JSON 格式并转发账号密码；用户 RPC 查询密码哈希、使用 bcrypt 验证密码、检查账户状态，再签发与旧服务使用相同 `user_id`、`issuer` 和 HS256 格式的 JWT。成功响应仍使用 `code`、`msg`、`data.token` 结构。新 Token 可用于此前的 `GET /api/v1/user/info`。

新登录与本人查询共用 RPC 的 `-profile` 开关、`USER_MYSQL_DSN` 和 `USER_JWT_SECRET`。本步 Token 有效期固定为 24 小时，与当前旧服务配置一致。API 进程不读取数据库密码或 JWT 密钥；旧 `:8080` 登录仍保留，方便逐步迁移。

错误凭证统一返回 HTTP 401；禁用账户只有在密码正确时才返回 403，错误密码不会透露账户状态。数据库或 RPC 不可用返回 503，超时返回 504。请求 JSON 无效、缺少字段或超过 4 KiB 返回 400。旧登录以 HTTP 200 携带业务错误码，新登录使用相应 HTTP 状态，客户端切换时需留意。

本地验证：`go test ./api ./rpc/user`。测试覆盖密码错误、禁用或不存在的用户、数据库错误、HTTP 参数校验，以及“新登录签发 Token → RPC 本人查询”的真实 gRPC 通信；数据库由 SQL 测试替身提供。尚未连接真实 MySQL，不能据此宣称生产登录链路已通过。新注册入口见 [用户注册](user-register.md)；真实数据库联调留在后续步骤。
