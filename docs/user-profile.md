# 小步迁移：查询真实的本人资料

本步新增 `GET http://127.0.0.1:8082/api/v1/user/info`。查询入口不接受用户 ID，而是携带旧登录接口签发的 Token。旧注册登录暂不迁移。

```text
旧 API 登录 → 得到 Token
带 Token 请求新 API
  → API 将凭证放入 RPC metadata（随 RPC 请求发送的附加信息）
  → 用户 RPC 验证签名、算法、签发者、有效期及用户 ID
  → 按 Token 中的 ID 只读查询 MySQL users 表
  → 检查账户状态 → 返回 id、username、nickname
```

本步把身份验证放在用户服务：API 先检查 Authorization 格式，RPC 再完整验签，因此直接绕过 API 调用 RPC 也不能通过指定 ID 读取资料。新接口拒绝所有查询参数，包括 `user_id`。只支持现有登录生成的 HS256、issuer 为 `go-im` 且包含有效期的 Token。

## 改了什么，为什么

| 文件 | 用途 |
| --- | --- |
| `api/profile.go` | 新增本人查询 Handler，转交 Token，不接受指定 ID |
| `api/main.go` | 注册新接口，复用已有 RPC 连接 |
| `api/handler.go` | 共用结果转换，补充 401 未认证、403 账户禁用 |
| `api/etc/api.yaml` | 关闭会记录请求头的框架请求日志，保留不含 Token 的错误日志 |
| `rpc/user/user.proto`、`pb/*` | 新增无用户 ID 参数的 GetMyInfo 方法及生成代码 |
| `rpc/user/profile.go` | 验证 Token，按认证身份查询，仅选择所需字段及账户状态 |
| `rpc/user/database.go` | RPC 自己建立 MySQL 连接池，限制等待时间 |
| `rpc/user/main.go`、`server.go` | 用 `-profile` 开启真实查询，将数据库连接和密钥交给服务 |
| `api/profile_test.go`、`rpc/user/profile_test.go` | 验证身份边界、数据库行为、RPC 凭证传递与 HTTP 返回 |

迁移期沿用旧 `users` 表，旧注册登录仍是用户数据的写入入口；新 RPC 只读，不建表、不迁移或修改数据。新服务不复用旧业务 Repository，不依赖旧全局数据库连接。逻辑库拆分与账号权限仍留待后续迁移。

## 启动

前提：现有 MySQL 和用户表可用，已有能够通过旧接口登录的账户。以下命令在项目根目录运行。

终端一，为 RPC 设置环境变量（用你自己的本地配置，勿提交密钥）：

```powershell
# DSN 对应旧 config/go-im.yaml 中的 mysql.dsn。
$env:USER_MYSQL_DSN = '<你的现有 MySQL DSN>'
# 必须与旧登录接口 jwt.secret 相同，API 进程不需要这个密钥。
$env:USER_JWT_SECRET = '<与旧登录一致的 JWT 密钥>'
go run ./rpc/user -profile
```

显式使用 `-profile` 才连接数据库；缺少环境变量或连接失败会退出，不伪装成查询成功。不带 `-profile` 仍能运行原演示；此时真实本人查询返回 503。

终端二启动新 API：

```powershell
go run ./api
```

可通过原 `POST http://127.0.0.1:8080/api/v1/user/login` 获得 Token；新增 `POST http://127.0.0.1:8082/api/v1/user/login` 也能签发兼容 Token，见 [用户登录](user-login.md)。注册新账号可使用 [新注册入口](user-register.md)。

在另一个 PowerShell 终端请求新接口（输入自己的登录 Token）：

```powershell
$profileToken = Read-Host '输入旧登录接口返回的 Token'
Invoke-RestMethod -Uri 'http://127.0.0.1:8082/api/v1/user/info' -Headers @{ Authorization = "Bearer $profileToken" }
```

成功响应格式如下，字段内容来自数据库：

```json
{"code":0,"msg":"success","data":{"id":"用户ID","username":"数据库中的用户名","nickname":"数据库中的昵称"}}
```

| 场景 | HTTP 状态 | code |
| --- | --- | --- |
| 有效 Token、用户存在且状态为 1 | 200 | 0 |
| 携带 user_id 或其他查询参数 | 400 | 10001 |
| 缺少、伪造、过期或不符合约定的 Token | 401 | 10002 |
| 用户状态不是 1（正常） | 403 | 20004 |
| Token 中的用户不存在 | 404 | 20002 |
| 未开启真实查询、RPC 不可用或数据库查询失败 | 503 | 10005 |
| RPC 等待超过 2 秒 | 504 | 10005 |

演示接口 `/demo/user/info?user_id=1` 保持返回固定演示数据。两个入口区别明确；不能用演示成功证明数据库已接通。

## 本步实际验证与限制

- `go test ./...` 通过，新旧包编译及相关测试通过，API、RPC 可执行文件编译通过。
- SQL 测试替身验证：查询 ID 来自 Token，SQL 不读取密码列；正常、禁用、不存在和数据库错误的处理。
- 无效凭证测试包含：缺少、重复、格式错误、签名错误、过期、无有效期、签发者错误、算法不符及非法用户 ID；这些情况下不能发起用户查询。
- API 测试验证：拒绝指定用户 ID、Token 传递、结果转交以及 401/403 错误映射。
- 使用真实本机 TCP 的 gRPC 测试验证 GetMyInfo 的凭证传递和响应，数据库仍为 SQL 测试替身。测试 Token 使用现有 `GenerateToken` 签发，以验证格式兼容。
- 原演示 API 与 RPC 的独立进程联调通过，包括连接失败、重启恢复和约 2 秒超时。
- **尚未完成真实 MySQL + 旧登录接口 + 新本人查询的联调**：用户已于 2026-09-20 提供腾讯云配置，本地已同步脱敏部署基线，并准备好新 API/RPC 的 Compose 配置，见 [部署说明](../deploy/README.md)。MySQL 和用户 RPC 只在 Compose 网络内暴露；新增容器尚未同步或启动到云端。测试替身不等于真实数据库，旧 IM 全链路也未验收。
- 本次只修改本地工作区，未连接或修改云端部署。

接通数据库后，应使用两个已有账户分别登录并检查返回各自资料，验证缺失或伪造 Token 被拒绝、指定他人 ID 被拒绝，以及数据库不可用时返回错误。完成这些实际检查后再更新项目验收状态。

本步新增测试依赖 `go-sqlmock v1.5.2`，不用于运行时数据库访问。JWT 校验选项参考 [golang-jwt 官方文档](https://golang-jwt.github.io/jwt/usage/parse/)，数据库查询使用已有 GORM 版本。
