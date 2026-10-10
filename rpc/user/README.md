# User RPC：用户与团队

负责注册登录、本人资料、团队与成员事实、基础角色、成员资格代际以及本人退出协调。Gateway、IM、Task 与 Agent 使用该服务核对身份和当前团队资格。

[整体架构](../../docs/architecture.md) · [HTTP 入口](../../api/README.md) · [部署](../../deploy/README.md)

## 当前能力与协议

- [user.proto](user.proto)：注册/登录、本人活动团队、成员列表/角色、群创建授权、成员解析、本人退出与操作查询、有限显示名补充。
- [trigger.proto](trigger.proto)：IM 后台触发使用的受限团队资格与负责人解析入口。
- [push.proto](push.proto)：Push 使用的受限当前团队资格入口。
- 普通本人方法从 Bearer Token 派生身份；后台方法使用专用 mTLS 身份和既定范围，不接受模型指定用户身份。

User 管理 `users`、`teams`、`team_members`、`user_team_leave_operations`。成员退出先进入 `leaving` 并保存固定操作，再调用 IM 关闭群资格，完成后置为退出；不清除旧聊天和阅读记录。拥有者再次添加已完成退出的成员时推进资格版本，旧群资格不自动恢复。

## 本地运行

从仓库根目录运行，先在本地环境中设置 `USER_MYSQL_DSN`、`USER_JWT_SECRET`，并准备当前数据库：

```sh
go run ./rpc/user -f rpc/user/etc/user.yaml -profile
go test ./rpc/user
```

配置默认监听 `127.0.0.1:9001`，Snowflake 节点默认 2，可用 `USER_SNOWFLAKE_NODE_ID` 设置。多个写入进程需使用不同节点；JWT 与 IM/WS 的实际配置一致。

**`-profile` 才启用数据库业务。** 不带该参数的 `GetUserInfo` 是保留的固定资料通信演示，不是正式用户服务启动方式。Compose 镜像已使用 `-profile`。

完整退出、后台触发和 Push 投递核权还需要专用证书和环境配置，通过[部署覆盖](../../deploy/README.md#完整功能部署)启用；普通 9001 端口不注册这些专用服务。

## 进一步阅读

[团队接口](../../docs/team-schema.md)、[负责人解析](../../docs/agent-assignee-design.md)、[资格代际](../../docs/stage7-team-membership-foundation-contract.md)、[本人退出协议](../../docs/stage7-team-leave-public-rpc-contract.md)、[重新入队](../../docs/stage7-team-rejoin-contract.md)。云端已测范围见[F6 报告](../../docs/frontend-f6-review.md)。

## 开发过程记录

下面保留早期逐步实现与验证细节，描述当时范围；当前启动方式与能力以上方说明为准。

<details>
<summary>展开历史实现记录</summary>

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

User可选配置`USER_LEAVE_IM_RPC_ADDR`、`USER_LEAVE_IM_TLS_CERT_FILE`、`USER_LEAVE_IM_TLS_KEY_FILE`、`USER_LEAVE_IM_TLS_CA_FILE`，四项必须同时设置，启用时需`-profile`。客户端验证IM服务端证书的精确DNS名`im.go-im.internal`，所用客户端证书须由IM专用退出监听认可为`user.go-im.internal`；普通User RPC不注册IMLeave。User只从033本人固定操作读取清理范围，在User事务外调用IM，收到持久关闭版本确认后才以新事务将成员和操作置为完成；详见[本步审查](../../docs/stage7-team-leave-user-cleanup-contract.md)。

普通User RPC现提供`LeaveTeam(team_id, request_key)`与`GetTeamLeaveOperation(team_id, request_key)`。两者只认`authorization: Bearer <token>`中的启用账号本人，`request_key`须为1—64位ASCII字母、数字、`-`或`_`；同键限定本人及原team。退出响应状态`0`待清理、`1`已完成；清理失败或回包丢失时本人可查询并用原键重试，完成后即使重新入队也可查询旧操作。未配置IM清理客户端时，首次退出在撤权前返回`Unavailable`，已完成操作仍可回显。Gateway/页面入口现已接线；基础Compose尚未配置专用证书，031—033也未在真实MySQL执行；部署时须先保证Push团队资格核权及IM清理链路就绪，再允许用户调用退出。见[公开入口审查](../../docs/stage7-team-leave-public-rpc-contract.md)。

拥有者使用现有`AddTeamMember`再次邀请旧成员时，User锁定目标成员行：`active`仍报重复，`leaving`拒绝，只有`left`且相同旧资格版本的退出操作已完成才恢复为普通成员、将版本加一并刷新加入时间。旧退出记录不删除，旧请求重放仍只指向旧代际。重新入队不自动加入任何旧团队群，本人需自行调用Join。[重入审查](../../docs/stage7-team-rejoin-contract.md)。

## Push专用团队资格协议（2026-10-06）

`push.proto`定义独立`UserPush.CheckPushTeamMember`，只核对指定用户的当前有效团队资格，并回显team/user/正generation。处理器要求精确`push.go-im.internal`的已验证TLS身份；普通User与UserTrigger端口均不能调用。Push还必须核对IM群成员及永久关闭版本，并在在线/离线投递前接入；详见[协议审查](../../docs/stage7-push-user-eligibility-contract.md)。

User进程现可选启用独立Push监听：设置`USER_PUSH_LISTEN_ON`、`USER_PUSH_TLS_CERT_FILE`、`USER_PUSH_TLS_KEY_FILE`、`USER_PUSH_TLS_CA_FILE`四项并使用`-profile`。监听仅注册UserPush，客户端证书须为精确`push.go-im.internal`，监听端口须与普通User和Trigger不同；四项全空默认关闭。异常停止专用监听会使User服务非零退出。基础Compose还未提供证书，Push消费者尚未接入；详见[监听审查](../../docs/stage7-push-user-listener-contract.md)。

## 团队负责人姓名解析（2026-10-03）

新增内部 RPC `ResolveTeamMember(team_id, name)`，用于后续 Agent 将讨论中的称呼匹配到真实团队成员；本步尚未接 Agent 或 HTTP/页面入口。

调用仍需 `authorization: Bearer <token>` metadata，先验证启用账号和当前团队成员资格，再查询本团队启用成员。输入姓名去除首尾空白后为 1—64 个 Unicode 字符，完整匹配用户名或昵称；SQL 使用 `CAST(... AS BINARY)` 比较，不按默认排序规则忽略大小写或重音，不使用模糊或通配符匹配。

响应 `candidates` 复用 `TeamMember` 资料，按用户 ID 升序，最多 20 个；查询第 21 个匹配确定 `truncated`。无匹配是成功的空结果，重名保留全部返回候选。同一成员匹配两个字段只返回一次，用户名不优先于他人的同名昵称。仅 `candidates` 长度为 1 且 `truncated=false` 才表示唯一；调用方不能默认选择第一位。此结果是解析当时的候选，正式指派仍须检查当前成员资格。

非法输入返回 `InvalidArgument`，无效身份返回 `Unauthenticated`，停用调用者或非团队成员返回 `PermissionDenied`，查询故障返回 `Unavailable`，不会把故障包装成未匹配。沿用现有团队表，无新增迁移。

验证命令：`go test ./rpc/user -run TestResolveTeamMember -count=1`、`go test ./... -count=1`。本轮定向与全量测试通过，Linux User 编译通过；SQL 替身检查完整范围/启用状态/二进制比较条件和绑定参数，本机 TCP gRPC 验证协议、身份与响应。未执行真实 MySQL 字符比较或接入真实模型；第一版模型负责人提取、歧义处理与本人选择仍待实现。方案、取舍及完整修改文件见[审查记录](../../docs/agent-assignee-design.md#7-user-解析接口实现与审查2026-10-03)。

</details>
