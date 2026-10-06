# 第一个用户 RPC：本地通信演示

后续小步已新增 GetMyInfo 本人查询、Login 登录和 Register 注册，使用 `-profile` 启用；见 [本人资料查询](../../docs/user-profile.md)、[用户登录](../../docs/user-login.md)、[用户注册](../../docs/user-register.md)。下文仍介绍不带 `-profile` 的原演示方式。

`CheckTeamMember(team_id)` 现除校验当前 Token 对应的可用用户仍属于团队外，成功时还返回该用户的 `user_id` 和当前团队 `role`（0 普通成员、1 管理员、2 拥有者）。这些值由用户与团队服务从登录身份和 `team_members` 表确定，调用方不传操作人 ID；非成员、无效 Token 或数据库故障仍返回原有错误。IM 现有调用只检查成功/失败，新字段供后续任务服务判断操作人权限；被指派人的成员资格仍需另设接口核对。

`CheckTeamMemberByID(team_id, user_id)` 现用于后续任务指派前核对指定成员。RPC 先从 Token 验证调用者仍属于该团队，再由用户与团队服务检查目标用户的团队成员记录和账号状态。目标不在团队返回 `NotFound`，账号停用返回 `FailedPrecondition`，调用者无团队资格返回 `PermissionDenied`，数据库故障返回 `Unavailable`。此接口只读，不代替任务服务的创建与状态权限判断。

本步目的：让两个独立程序通过 RPC 通信。客户端暂时代替未来的 API，服务端只返回固定演示资料。

```text
验证客户端 → gRPC 网络调用 → 用户服务 GetUserInfo → 固定演示资料
```

本步未接入 HTTP、JWT、MySQL，也未迁移旧接口。`user_id` 只是演示查询参数，不构成身份授权；服务只监听 `127.0.0.1:9001`。后续接入真实数据前，需要确定可信调用身份及业务权限校验。

## 建议审查顺序

| 文件 | 用途 |
| --- | --- |
| `user.proto` | 约定方法名、请求和响应字段；没有密码字段 |
| `server.go` | 实现查询：ID 必须为正数；只有演示用户 1 存在 |
| `main.go` | 读取配置，注册查询方法，启动 go-zero RPC 服务 |
| `etc/user.yaml` | 指定本机监听地址和 2 秒服务端超时 |
| `client/main.go` | 连接 RPC，发起请求，输出结果或错误；调用最多等待 2 秒 |
| `pb/*.pb.go` | 由工具生成的消息结构和 RPC 调用代码，不手工修改 |

先把 `.proto` 理解成双方约定的接口说明；`pb` 中的生成代码负责把这个约定变成可调用的 Go 代码。生成方式参考 [gRPC 官方教程](https://grpc.io/docs/languages/go/quickstart/)。

## 启动与验证

以下命令均在仓库根目录运行。已有生成文件，运行服务不需要先安装生成工具。

终端一启动服务（保持运行）：

```powershell
go run ./rpc/user -f rpc/user/etc/user.yaml
```

终端二发起查询：

```powershell
go run ./rpc/user/client -id 1
```

应得到如下内容（空格可能不同；Protobuf JSON 用字符串表示 int64）：

```json
{"id":"1","username":"demo_user","nickname":"演示用户（非真实数据）"}
```

客户端可能同时输出 go-zero 的连接统计日志，包含上述三个字段的 JSON 才是本次查询结果。

验证错误场景：

```powershell
go run ./rpc/user/client -id 0
# InvalidArgument：ID 必须为正数。

go run ./rpc/user/client -id 2
# NotFound：演示用户不存在。
```

在终端一按 Ctrl+C 停止服务，再运行正常查询命令，应报连接错误（通常为 `Unavailable`；达到等待上限时可能为 `DeadlineExceeded`），退出码非零，而不是返回演示资料。这个对照证明资料来自另一个程序。

若端口被占用，调整 `etc/user.yaml` 中的 `ListenOn`，客户端用 `-addr 127.0.0.1:新端口` 指定同一地址。

## 本步验证记录（2026-09-18）

- `go test ./...` 通过：现有 JWT、认证中间件测试通过，其他包编译通过；新增 RPC 没有单元测试，本步用独立进程进行实际调用验证。
- 服务端与客户端分别编译为可执行文件后，实际启动服务，通过 TCP 调用：ID 1 成功；ID 0、-1 返回 `InvalidArgument`；ID 2 返回 `NotFound`，错误调用均非零退出。
- 终止本次启动的 RPC 进程后，再查询 ID 1，返回 `Unavailable`，非零退出。
- 用只监听 TCP、不响应 gRPC 的本地端口模拟无响应，客户端返回 `DeadlineExceeded`，非零退出。
- 验证结束后已停止本次启动的服务。未进行 HTTP、数据库或旧 IM 全链路运行验证。

引入 go-zero v1.10.3、gRPC v1.80.0 后，Go 的依赖版本选择同步调整了部分原有公共库（包括 Redis 客户端、WebSocket 库、MySQL 驱动等）。具体变更见根目录 `go.mod` / `go.sum`。依赖中的 Etcd、Kubernetes 等包来自框架，不代表本步部署了这些组件。

## 重新生成代码（仅修改 proto 后需要）

本次工具版本：protoc 31.1（版本输出为 `libprotoc 31.1`，生成文件标记为 v6.31.1）、protoc-gen-go v1.36.11、protoc-gen-go-grpc v1.5.1。运行依赖固定在根目录 `go.mod` / `go.sum`。

当前工作区工具位于 Git 忽略的 `bin/rpc-tools`，不随仓库提交。新环境可从 [Protobuf 官方发布页](https://github.com/protocolbuffers/protobuf/releases/tag/v31.1) 下载相应系统的编译器，并用下面命令安装 Go 插件：

```powershell
New-Item -ItemType Directory -Path bin/rpc-tools -Force | Out-Null
$env:GOBIN = Join-Path (Get-Location) 'bin/rpc-tools'
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
```

当前 Windows 工作区重新生成命令：

```powershell
$env:PATH = (Join-Path (Get-Location) 'bin/rpc-tools') + ';' + $env:PATH
./bin/rpc-tools/protoc-31.1/bin/protoc.exe --proto_path=rpc/user --go_out=rpc/user/pb --go_opt=paths=source_relative --go-grpc_out=rpc/user/pb --go-grpc_opt=paths=source_relative user.proto
```

其他环境把命令开头换成自己的 `protoc` 路径即可。

## 团队退出内部清理协调（2026-10-06）

User可选配置`USER_LEAVE_IM_RPC_ADDR`、`USER_LEAVE_IM_TLS_CERT_FILE`、`USER_LEAVE_IM_TLS_KEY_FILE`、`USER_LEAVE_IM_TLS_CA_FILE`，四项必须同时设置，启用时需`-profile`。客户端验证IM服务端证书的精确DNS名`im.go-im.internal`，所用客户端证书须由IM专用退出监听认可为`user.go-im.internal`；普通User RPC不注册IMLeave。User只从033本人固定操作读取清理范围，在User事务外调用IM，收到持久关闭版本确认后才以新事务将成员和操作置为完成。当前只是包内方法，没有对外退出/恢复RPC，也未配置基础Compose；详见[本步审查](../../docs/stage7-team-leave-user-cleanup-contract.md)。

## Push专用团队资格协议（2026-10-06）

`push.proto`定义独立`UserPush.CheckPushTeamMember`，只核对指定用户的当前有效团队资格，并回显team/user/正generation。处理器要求精确`push.go-im.internal`的已验证TLS身份；普通User与UserTrigger端口均不能调用。Push还必须核对IM群成员及永久关闭版本，并在在线/离线投递前接入；详见[协议审查](../../docs/stage7-push-user-eligibility-contract.md)。

User进程现可选启用独立Push监听：设置`USER_PUSH_LISTEN_ON`、`USER_PUSH_TLS_CERT_FILE`、`USER_PUSH_TLS_KEY_FILE`、`USER_PUSH_TLS_CA_FILE`四项并使用`-profile`。监听仅注册UserPush，客户端证书须为精确`push.go-im.internal`，监听端口须与普通User和Trigger不同；四项全空默认关闭。异常停止专用监听会使User服务非零退出。基础Compose还未提供证书，Push消费者尚未接入；详见[监听审查](../../docs/stage7-push-user-listener-contract.md)。

## 团队负责人姓名解析（2026-10-03）

新增内部 RPC `ResolveTeamMember(team_id, name)`，用于后续 Agent 将讨论中的称呼匹配到真实团队成员；本步尚未接 Agent 或 HTTP/页面入口。

调用仍需 `authorization: Bearer <token>` metadata，先验证启用账号和当前团队成员资格，再查询本团队启用成员。输入姓名去除首尾空白后为 1—64 个 Unicode 字符，完整匹配用户名或昵称；SQL 使用 `CAST(... AS BINARY)` 比较，不按默认排序规则忽略大小写或重音，不使用模糊或通配符匹配。

响应 `candidates` 复用 `TeamMember` 资料，按用户 ID 升序，最多 20 个；查询第 21 个匹配确定 `truncated`。无匹配是成功的空结果，重名保留全部返回候选。同一成员匹配两个字段只返回一次，用户名不优先于他人的同名昵称。仅 `candidates` 长度为 1 且 `truncated=false` 才表示唯一；调用方不能默认选择第一位。此结果是解析当时的候选，正式指派仍须检查当前成员资格。

非法输入返回 `InvalidArgument`，无效身份返回 `Unauthenticated`，停用调用者或非团队成员返回 `PermissionDenied`，查询故障返回 `Unavailable`，不会把故障包装成未匹配。沿用现有团队表，无新增迁移。

验证命令：`go test ./rpc/user -run TestResolveTeamMember -count=1`、`go test ./... -count=1`。本轮定向与全量测试通过，Linux User 编译通过；SQL 替身检查完整范围/启用状态/二进制比较条件和绑定参数，本机 TCP gRPC 验证协议、身份与响应。未执行真实 MySQL 字符比较或接入真实模型；第一版模型负责人提取、歧义处理与本人选择仍待实现。方案、取舍及完整修改文件见[审查记录](../../docs/agent-assignee-design.md#7-user-解析接口实现与审查2026-10-03)。
