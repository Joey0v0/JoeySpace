# 最小 API：用 HTTP 调用用户 RPC

Gateway 的 `/demo/chat` 提供同源原生 HTML/JavaScript 演示页。页面复用登录 Token 和团队 ID，可分页查看团队任务、人工创建任务、修改状态、按来源 ID 查看原群消息。创建失败重试会保留请求键；状态和原消息的权限仍由任务/IM RPC 判定。页面逻辑通过本地 Node 测试，尚未做真实浏览器、MySQL 或容器联调。

人工创建任务入口为 `POST /api/v1/teams/{team_id}/tasks`，需要 Bearer Token 与 `Idempotency-Key`（规则同团队群创建）。请求体示例：`{"title":"检查缓存","description":"复现并修复问题","assignee_id":"123","source_group_id":"300","source_message_id":"400","due_at_unix_ms":1790874000000}`；三个 ID 字段均为可省略的十进制字符串，两个来源 ID 必须同时填写或同时省略，避免浏览器大整数丢精度。`due_at_unix_ms` 是可省略的 JSON 整数，表示 UTC Unix 毫秒时间点，省略或传 0 表示无截止时间；非零值须在 1～253402300799999 之间。标题 1～200 字符，说明最多 2000 字符。成功时 `data.task_id` 是字符串。同键同创建请求返回原任务 ID，同键不同内容返回 HTTP 409。Gateway 仅转发到任务 RPC；任务服务向用户与团队 RPC 核对成员，并在有来源时向 IM RPC 核对消息归属和访问权，然后写任务表。已有数据库须执行 006、007、008、009 迁移；本地自动化测试通过，真实 MySQL/容器链路尚未验收。演示页创建表单能输入本地日期时间；任务列表也会按浏览器本地时区显示截止时间。

团队任务列表入口为 `GET /api/v1/teams/{team_id}/tasks?after_task_id=0&limit=20`，需要 Bearer Token；两个查询参数可省略，默认 20 条、最多 100 条。结果按任务 ID 升序，`task_id`、`creator_id`、`assignee_id`、`source_group_id`、`source_message_id`、`next_after_task_id` 均为字符串；未指派或无来源的对应 ID 为 `"0"`，下一页游标为 `"0"` 表示无后续数据。每项任务的 `due_at_unix_ms` 是 JSON 数字，表示 UTC Unix 毫秒；`0` 表示未设置。只有当前团队成员可读；消息正文仍由 IM 校验群访问权。

状态更新入口为 `PUT /api/v1/teams/{team_id}/tasks/{task_id}/status`，需要 Bearer Token，请求体如 `{"status":1}`；状态 0 待办、1 进行中、2 完成。仅创建者、当前负责人或团队拥有者可改，重复提交当前状态仍返回成功。任务 RPC 在同一事务中更新状态并写操作记录；已有数据库须在 006 后执行 007 增量迁移。列表与状态入口已通过本地测试，真实 MySQL/容器链路尚未验收。

后续小步已新增 `/api/v1/user/info` 本人查询（需 Token）、`/api/v1/user/login` 登录、`/api/v1/user/register` 注册，以及创建团队、添加成员、查询成员和变更角色的团队入口；接口与验证限制见 [本人资料查询](../docs/user-profile.md)、[用户登录](../docs/user-login.md)、[用户注册](../docs/user-register.md)、[团队数据与接口](../docs/team-schema.md)。团队群创建 HTTP 入口为 `POST /api/v1/teams/{team_id}/groups`，需 Bearer Token 与 Idempotency-Key（1～64 位 ASCII 字母、数字、-、_、.）；同一次创建重试复用该键，请求体为 `{"name":"项目群"}`，成功返回字符串形式的 `data.group_id`。Gateway 只转发给 IM RPC；IM 再向用户与团队 RPC 检查创建权限并写群。相同键和内容的重试返回原群 ID，同键不同内容返回 HTTP 409。已有数据库需执行 004 增量迁移；该入口尚未做真实 MySQL 联调。下文记录原演示接口及其验证。

团队群列表入口为 `GET /api/v1/teams/{team_id}/groups?after_group_id=0&limit=20`，需要 Bearer Token；`after_group_id` 和 `limit` 可省略，默认从头读取 20 条，最多 100 条。响应中的 `group_id`、`owner_id`、`next_after_group_id` 为字符串；下一页使用返回的非零 `next_after_group_id`。IM 会向用户与团队 RPC 核对团队成员资格；看到群目录不表示已加入这些群。

加入团队群入口为 `POST /api/v1/teams/{team_id}/groups/{group_id}/join`，只需携带本人 Bearer Token，不用请求体。仅当前团队成员可加入所属团队的群；重复加入仍返回成功。旧群或其他团队的群返回 404。IM 负责核对身份、团队资格和群归属，Gateway 不直接写群成员表。

团队群历史入口为 `GET /api/v1/teams/{team_id}/groups/{group_id}/messages?before_message_id=0&limit=20`，需要 Bearer Token。游标与数量可省略，默认读最新 20 条，最多 100 条；结果按消息 ID 倒序。只有该群成员且仍属于对应团队时可读。响应中的消息 `id`、`from_id` 和 `next_before_message_id` 是字符串；非零游标用于继续读更旧消息。`created_at_unix_ms` 是毫秒时间戳。已有数据库需先执行 005 增量迁移；尚未做真实 MySQL 联调。

聊天演示页由 Gateway 在 `GET /demo/chat` 提供，例如 `http://127.0.0.1:8082/demo/chat`。登录后填写团队 ID，团队拥有者可输入群名并点击“Create group”；页面首次创建时生成请求键，失败重试沿用同一个键，新建另一群才点击“New key”。成功后会选中新群并填入发送目标。其他团队成员可分页加载群目录，选择群后点击“Join current group”用本人 Token 入群，再发送消息、查看历史。目录可见不代表已入群，服务端仍核对团队与群成员资格。页面的登录、群创建/目录/本人入群、离线拉取与确认、团队群历史均使用相对 HTTP 路径，请从 Gateway 地址打开页面；WebSocket 仍独立连接，未填写 WS 地址时默认使用页面所在主机的 8081 端口，也可手动指定。页面已嵌入 Gateway 可执行文件，无需单独复制 HTML。页面和 HTTP 接口的本地测试及 Gateway 页面 GET 已通过；真实浏览器、数据库和 WS/Kafka 链路尚未联调。

这一步把上一小步的命令行调用接成 HTTP 接口：

```text
浏览器 / curl
  → GET 127.0.0.1:8082/demo/user/info?user_id=1
  → API 解析参数，通过 RPC 查询用户
  → 127.0.0.1:9001 用户服务返回固定演示资料
  → API 转成 HTTP JSON 响应
```

API 不生成演示资料、不访问数据库。请求仍未接入登录认证，`user_id` 不构成用户身份证明；本步仅提供本机演示接口。旧 Gin API 继续使用原来的入口与接口。

## 审查顺序

| 文件 | 职责 |
| --- | --- |
| `main.go` | 读配置，建立可复用的 RPC 客户端，注册并启动 HTTP 接口 |
| `handler.go` | 校验查询参数，调用 RPC，把成功结果或 RPC 错误转换为 HTTP 响应 |
| `etc/api.yaml` | API 监听 8082，RPC 地址为 9001；RPC 等待 2 秒，HTTP 等待 3 秒 |
| `handler_test.go` | 验证非法参数不调用 RPC、结果来自 RPC、错误状态转换及内部错误详情不外传 |

`main.go` 的路由交给 `handler.go`；Handler 使用已有 `rpc/user/pb` 中的客户端访问另一个进程。真实业务查询仍由用户 RPC 实现。本步直接使用已固定版本的 go-zero `rest`、`zrpc`，没有生成大套目录。

## 在项目根目录运行

终端一：

```powershell
go run ./rpc/user
```

终端二：

```powershell
go run ./api
```

终端三（也可在浏览器打开此地址）：

```powershell
curl.exe -i 'http://127.0.0.1:8082/demo/user/info?user_id=1'
```

正常返回 HTTP 200：

```json
{"code":0,"msg":"success","data":{"id":"1","username":"demo_user","nickname":"演示用户（非真实数据）"}}
```

ID 用字符串返回，避免后续较大的用户 ID 在浏览器中损失精度。此演示 API 的 HTTP 错误状态与旧接口不同：旧接口常用 HTTP 200 配合业务错误码；新接口同时返回对应 HTTP 状态及 `code`、`msg`。

| 场景 | HTTP 状态 | JSON code |
| --- | --- | --- |
| ID 1 | 200 | 0 |
| 缺少、重复、非整数、非正数或超出 int64 范围的 ID | 400 | 10001 |
| ID 2（用户不存在） | 404 | 20002 |
| RPC 未启动或停止 | 503 | 10005 |
| RPC 调用超时 | 504 | 10005 |
| 其他 RPC 错误 | 502 | 10005 |

错误响应不包含 `data`，例如：

```json
{"code":10005,"msg":"user service unavailable"}
```

RPC 的详细错误保存在 API 日志中。RPC 客户端以非阻塞方式创建，因此 RPC 未启动时 API 仍可启动，请求时返回 503；RPC 恢复后客户端会重连，重连期间请求仍可能暂时失败。

## 手动验证

```powershell
curl.exe -i 'http://127.0.0.1:8082/demo/user/info?user_id=abc'
curl.exe -i 'http://127.0.0.1:8082/demo/user/info?user_id=2'
```

在终端一停止 RPC，保留 API 运行，再查询 ID 1，应得到 503。重新启动 RPC，等待重连后再次查询，应恢复 200。这能直观看到 HTTP 接口确实依赖另一个进程提供结果。

自动化测试：

```powershell
go test ./api -v
```

这些测试用替身模拟 RPC 结果，不需要启动两个服务。真实跨进程验证需要按前述命令运行。此步不代表真实个人信息查询、注册登录或第一阶段整体验收完成。

## 本步验证记录（2026-09-18）

- `go test ./...` 通过，包括新增 API 测试和原有 JWT、认证中间件测试；API、RPC 可执行文件编译通过。
- 独立启动 API 和 RPC，经 HTTP 查询用户 1 返回 200 及 RPC 演示资料；缺失、空值、非整数、非正数、重复及越界 ID 返回 400，用户 2 返回 404。
- API 可以在 RPC 未启动时启动，查询返回 503；RPC 启动后恢复 200。再次停止 RPC 返回 503，重启 RPC 后恢复 200，过程中未重启 API。
- 使用只监听 TCP、不响应 gRPC 的本地端口模拟无响应，HTTP 约 2.06 秒返回 504 及 JSON 错误。
- 验证结束后已停止本次启动的进程。真实数据库、登录鉴权及旧 IM 全链路未做运行验收。

本步复用已有依赖版本，只在根目录 `go.mod` / `go.sum` 补入 go-zero HTTP 模块所需的 `github.com/golang-jwt/jwt/v4 v4.5.2` 间接依赖；已有 JWT v5 仍保留。这不是认证功能的实现。

## 阶段 5：显式只读问答入口

`POST /api/v1/teams/{team_id}/groups/{group_id}/ask` 接收 `Authorization: Bearer <Token>` 和 JSON `{"question":"..."}`；成功返回 `{"code":0,"msg":"success","data":{"answer":"..."}}`。团队/群 ID 从路径解析为正整数，问题去除首尾空白后限制为 1～2000 字符；Gateway 不读取 IM/任务数据库，不判断群成员资格，只把原 Token 与请求转给 Agent RPC。Agent 再经 IM 和 Task RPC 校验实际访问权限。此入口仅向请求者返回答案，不写群消息或任务。

问答路由单独允许 22 秒、Agent RPC 客户端等待 21 秒、Agent 内部总超时 20 秒；其他 Gateway 路由仍使用原有 3 秒配置。权限拒绝返回 403、Agent 不可用返回 503、超时返回 504；内部 RPC 或模型错误详情不会写入 HTTP 响应。Agent 容器尚在可选 `agent` profile 中，未启用时本入口预计返回 503。本地跨进程测试使用 Gateway 测试 HTTP 服务器、独立 Agent gRPC 子进程、假模型及 IM/Task TCP gRPC 替身，覆盖成功、IM 拒绝和 Task 不可用；尚未请求真实模型、连接真实业务服务或运行容器。

`/demo/chat` 现在有独立的只读 Ask AI 区域：复用页面已有 Token、团队 ID 与当前群 ID，成功后只在该区域以文本显示答案；请求失败保留问题供重试，切换上下文时不显示旧请求的答案。页面不会因问答调用群发送或任务创建接口。Node 页面测试及 Gateway 的 Go 测试通过；真实浏览器和模型调用仍未验收。

## 阶段 6：任务草稿入口

`POST /api/v1/teams/{team_id}/groups/{group_id}/task-drafts` 接收原用户 `Authorization: Bearer <Token>`、`Idempotency-Key` 和 JSON `{"instruction":"..."}`。Gateway 校验团队群 ID、1～64 位请求键和去除首尾空白后 1～2000 字的指令，再把原 Token、请求键与范围交给 Agent 的 `PrepareTaskDraft` RPC；成功时以十进制字符串返回 `data.run_id`。同一指令在响应丢失后重试必须沿用请求键，新的一次生成须换键；异内容复用键返回 409。生成路由超时 22 秒，Agent RPC 客户端等待 21 秒，Agent 内部总超时 20 秒。

`GET /api/v1/agent/runs/{run_id}/draft` 携带原 Token，经 Agent 的 `GetTaskDraft` RPC 重新核对发起人及当前团队群资格。响应中 `run_id`、`team_id`、`group_id`、负责人、来源消息及任务 ID 均为字符串，避免浏览器丢失 64 位精度；截止时间仍为 UTC 毫秒数。读取路由超时 15 秒。Gateway 不直接读取 Agent 数据库，也不凭页面里的团队群 ID 判定权限。`/demo/chat` 新增生成与读取草稿区域，仅以文本展示结果；切换团队群或修改指令时不展示旧请求结果。此区域已支持标题/说明编辑及明确确认/重试按钮，创建结果用任务 ID 展示。Gateway 及 Node 页面替身测试通过，真实方舟、MySQL、容器与浏览器联调仍未验收。

`PUT /api/v1/agent/runs/{run_id}/draft` 携带原 Token，修改标题和说明。JSON 必须显式提供 `title`、`description`、`expected_title`、`expected_description`、`expected_revision` 五个字符串；空说明写 `""`，不能省略或使用 `null`。例如：

```json
{"title":"整理会议记录","description":"补充讨论结论","expected_title":"会议记录","expected_description":"","expected_revision":"1"}
```

旧标题/说明及 `expected_revision` 从最近一次 GET 的 `data.draft` 取得，Gateway 转交；版本为 1～9223372036854775807 的规范十进制字符串，不能省略、使用 JSON 数字/null、正号或前导零。响应 `data.draft.revision` 同样为字符串，避免 JavaScript 丢失精度；新标题去除首尾空白后为 1～200 字，新说明最多 2000 字。HTTP 请求体上限 32 KiB，支持字段到达上限时的 Unicode 转义写法。Gateway 只校验输入并调用 `EditTaskDraft`，Agent 核对发起人、当前团队群资格和待确认状态；成功响应与 GET 相同，整数 ID 保持字符串。路由超时 15 秒。

旧内容变化或草稿不再待确认时返回 HTTP 409、业务码 `80001`，不会自动覆盖；客户端应重新 GET 并审查后再提交。响应丢失时也先读取核对最终内容。无权限为 403、不存在或属于他人的运行为 404、Agent 不可用为 503、超时为 504，内部错误详情不写入响应。此 HTTP 入口无需生成草稿时的请求键，只修改已有草稿文字。HTTP 处理器与 RPC 替身测试通过，页面操作见下文；真实 MySQL 和容器联调仍未完成。并发旧值取舍见 A40，现已按用户确认 A50 增加版本保护，详见[架构记录](../docs/architecture-decisions.md)。

`/demo/chat` 的草稿区域现可编辑标题和说明：先 Prepare/Load 获取当前草稿，再点击 Save draft 经同源 PUT 保存；身份、团队群、运行 ID 与最近读取快照绑定，非待确认状态不能保存。发生 409 或保存结果不确定时保留输入，禁用保存并提示重新 Load；最新服务端草稿展示在结果区，用户实际修改过的字段留在编辑框供比对，未改字段同步服务端。保存成功更新旧值基准，保存期间继续输入的更新留待下次提交。页面切换账号/团队群/运行 ID 时清除旧编辑状态，不自动重试 PUT 或创建正式任务。Node 49 项测试和 Gateway 页面嵌入测试通过；真实浏览器及后端联调仍未验收。

### 明确确认与任务结果

`POST /api/v1/agent/runs/{run_id}/confirm` 接收原 Token，以及最近读取的已保存文字：

```json
{"expected_title":"整理会议记录","expected_description":"","expected_revision":"1","expected_assignee_id":"0","expected_due_at_unix_ms":0}
```

文字及版本三字段必须显式提供字符串，版本规则同 PUT；空说明使用 `""`，不能省略或为 `null`。新增 `expected_assignee_id` 必须为规范非负十进制字符串，显式 `"0"` 和缺失不同；含称呼或人工选择的草稿由 Agent 要求提供，旧草稿/无称呼兼容缺失，新页面始终提交。标题最多 200 字且非空，说明最多 2000 字；旧值原样传递，不去除空白。请求体上限 32 KiB。仅 Agent 决定 Task 创建键；HTTP `Idempotency-Key` 不使用也不转发，正文不能指定创建键、用户 ID 或团队群范围。确认路由预算现为 19 秒，Agent 整体 18 秒；创建阶段保留 12 秒、其中 Task 调用最多 5 秒，回帖阶段最多 5 秒、IM 调用最多 4 秒。Agent 服务端 20 秒与此处 Agent RPC 客户端 21 秒容纳该调用链，较短调用方截止时间仍优先。成功须返回已持久记录的 `succeeded` 和正任务 ID，例如：

```json
{"code":0,"msg":"success","data":{"run_id":"9001","team_id":"2","group_id":"3","status":"succeeded","task_id":"9223372036854775806","draft":{"revision":"1","title":"整理会议记录","description":"","assignee_id":"0","due_at_unix_ms":0,"source_message_id":"0"}}}
```

GET/PUT/确认响应的 `data.task_id` 均为字符串。待确认或 `creating` 时为 `"0"`，成功时为正整数；缺少成功 ID、状态与结果矛盾或范围不完整的 RPC 响应返回 502，不假报成功。

| 确认场景 | HTTP / 业务码 | 客户端处理 |
| --- | --- | --- |
| 旧版本、旧文字或状态冲突 | 409 / 80001 | 重读同一运行并审查 |
| Task 创建键冲突 | 409 / 70001 | 重读核对，不能自行换键补建 |
| 未登录 / 无当前资格 / 不存在或非本人 | 401 / 403 / 404，沿用对应业务码 | 不越权重试；不据此断定较早的创建未成功 |
| 服务不可用 / 超时 / 异常结果 | 503 / 504 / 502 | 结果可能未记录，先 GET 同一运行，再显式重试 |

页面新增“Confirm and create task”：未保存文字不能确认，请求期间不能同时生成、读写或再次确认。任何确认结果不确定先禁用编辑和确认，保留运行 ID，用户点击 Load 核对；读取到 `creating` 后显示“尚未记录结果，无自动后台重试”并允许“Retry confirmation”，继续用同一运行和固定内容；读取成功状态显示任务 ID并锁定草稿。冻结状态显示服务端已提交内容，不保留旧编辑框中的未保存文字。没有自动确认或自动重试。2026-10-03 已接入独立回帖字段和 HTTP 重试操作，任务成功不能当作回帖受理或全部送达；详见下节。

### 回帖状态与显式重试（2026-10-03）

草稿 GET/确认及回帖重试响应追加 `data.reply_status`、`data.reply_msg_id`，任务 ID 仍用字符串。状态为 `disabled`、`not_started`、`pending`、`accepted`、`unknown`；后两项分别表示已保存 IM 受理结果、确认后的回帖准备结果不确定。`pending` 已存固定意图但可能已被 IM 受理；`accepted` 不保证历史已保存或每位成员已收到。pending/accepted 必须对应同一成功运行的 `bot-task:<run_id>`；其他状态不能夹带消息 ID。未知状态、不一致任务状态或错消息 ID 返回 502。旧 Agent 完全省略两字段时兼容原响应，页面显示状态不可用并禁用重试，不假定未开始或已受理。

`POST /api/v1/agent/runs/{run_id}/reply/retry` 使用当前 `Authorization: Bearer <Token>`，请求体和查询参数必须为空；路径只含原运行 ID。Gateway 丢弃客户端创建键，不直接查询数据库、构造卡片或判断群资格，只转调 Agent `RetryTaskReply`。Agent 重查发起人及当前团队群资格，再使用持久成功任务的原回帖；不会调用 Task 或模型。此路由 19 秒、Agent 整体 18 秒，其他已有读写路由保持原预算。

成功需返回 `succeeded`、正任务 ID、`reply_status=accepted` 及匹配消息 ID。冲突/不满足重试前提返回 409/80001；身份与范围错误沿用 401/403/404，依赖不可用/超时返回 503/504，异常成功结果返回 502。错误响应不是“任务未创建”或“消息未发送”的证据：页面保留已知任务结果，要求 Load 同一运行核对后显式重试。

```sh
curl -X POST 'http://localhost:8082/api/v1/agent/runs/9001/reply/retry' \
  -H 'Authorization: Bearer <Token>'
```

页面把创建任务与回帖分开：确认返回 pending/unknown 时不自动重读或重发；点击 Load 后仅 not_started/pending 可以操作“Send group reply”/“Retry group reply”。操作期间与生成、读写草稿、确认互斥，重复点击不多发；失败须再次成功重读才恢复操作。已受理、关闭、未知和旧响应状态不可重试。切换 Token、团队群、运行或在请求期间换回旧上下文，都不会展示旧响应为当前成功。真实浏览器及消息链验收仍待最终部署。

HTTP/实际 Agent/本机 mTLS 客户端联调用 SQL 与业务替身验证确认后不确定回帖、受理保存失败、重读原消息、已受理重放及撤销权限；Task 创建次数为一次。页面 Node 77 项通过，不等于浏览器或真实数据库/broker 验收。完整文件清单及验证见[本轮审查记录](../docs/agent-group-reply-design.md#gateway-与页面回帖接线2026-10-03)。

本轮 Node 页面 58 项、HTTP 测试与全量 Go 回归通过；本机联调用真实 HTTP/TCP gRPC、实际 Agent 确认逻辑、User/IM/Task 和 SQL 替身验证响应丢失后同键重试及成功后权限撤销。未运行真实浏览器、生产进程、业务服务、MySQL 或容器，未请求模型。沿用[架构记录 A41](../docs/architecture-decisions.md)；[完整审查清单](../docs/agent-confirmation-design.md#7-http与页面确认实现的修改文件与验证)。

## 消息发送者身份（阶段 6，A43）

团队群历史、离线消息和 WebSocket 下行均新增 `sender_type` 和 `initiator_id`。`sender_type=1` 表示用户，`from_id` 是用户 ID，`initiator_id="0"`；`sender_type=2` 表示 IM 机器人，`from_id` 是机器人 ID，`initiator_id` 是本次授权用户的正整数字符串。所有 ID 继续使用字符串。Gateway 兼容旧 IM 未提供类型的响应，归为用户消息；旧 Gin 离线出口也保留这些字段。

原生页面对实时、历史、离线及来源预览显示同样的身份标签；机器人显示自身 ID 与确认用户 ID，尚未查询机器人显示名。未知或异常身份不会伪装为已识别的 AI 助手。仍按 `msg_id` 去重，离线重复消息仍正常确认。普通 WS 上行不能指定机器人身份，后端固定为当前用户。Push 的机器人群消息包含全部当前成员，不因机器人 ID 与某个用户 ID 数值相同而跳过该用户。

2026-10-02 全量 Go 测试和 Node 页面 62 项通过。该步骤贯通身份字段，没有开放机器人发送接口或实现结构化任务卡片；真实 MySQL/Kafka/Redis/浏览器/容器未验收。已有库要求 013 迁移，启用回帖前须让相关服务和客户端都具备新字段支持；[完整修改及验证](../docs/agent-group-reply-design.md#身份字段传输与展示实现记录2026-10-02)。

### 草稿版本升级与页面写入

Agent、Gateway、原生页面须协调更新版本契约。已迁移的旧草稿可读取，但旧客户端缺少 expected_revision 的写入返回 400（RPC 为 InvalidArgument），不自动补当前版本。旧 Agent 读取响应省略 revision 时保留只读展示，页面禁用保存/确认；编辑成功必须有正版本，确认结果版本必须与请求一致，异常返回 502。

页面保存/确认使用最近成功读取或保存响应的版本字符串；冲突保留输入并要求 Load，重读后才以新版本再次提交。内容改后恢复仍能识别过时窗口，无变化保存版本不变；冻结重试沿用固定版本和原 Task 键。Node 80 项及全量 Go 通过，真实浏览器、MySQL/017 迁移、容器与云端尚未验收。[本轮完整审查](../docs/agent-assignee-design.md#10-草稿版本基础与现有写入接线审查2026-10-03)。

### 负责人展示与本人选择（2026-10-03）

共享草稿响应始终输出 `assignee_name` 和 `assignee_resolution` 字符串，包括空称呼及旧数据的空状态，ID/版本仍使用十进制字符串。页面展示原称呼、实际 ID、解析或人工选择状态；`matched` 供本人审查，`not_found/ambiguous/truncated` 必须先处理，`selected/unassigned` 是已保存的人工选择。非法状态/称呼/ID 组合返回 502，不据此开放确认。

`PUT /api/v1/agent/runs/{run_id}/draft/assignee` 转调专用 Agent RPC，使用原登录 Token，15 秒路由预算。正文上限 1 KiB，仅接受两个必填字符串：

```json
{"assignee_id":"123","expected_revision":"2"}
```

`assignee_id` 允许 `"0"` 明确未指派，版本必须正；拒绝数值、null、负数、前导零、越界或额外字段。成功返回完整待确认草稿，ID/状态与请求一致，版本为原版本或加一；冲突返回 409，依赖错误沿用已有映射，异常成功结果返回 502。Gateway 不查数据库或决定成员权限。

页面通过显式按钮加载已有团队成员分页目录，每页最多 100；选择非零须来自目录，下拉框区分未选择与未指派。未保存文字或选择不能确认；负责人保存与其他草稿操作互斥。冲突/不确定结果保留输入并要求重新 Load；账号、团队群或运行切换后旧结果不会重新开放写入。确认提交最近快照中的负责人 ID，冻结后的重试保留该 ID 与版本。旧响应完全缺少两元数据字段时兼容原契约，不把部分缺失当作已审查。完整验证和文件见[本批审查](../docs/agent-assignee-design.md#11-负责人选择闭环与并行集成审查2026-10-03)。

### 草稿截止时间编辑与确认审查（2026-10-03）

`PUT /api/v1/agent/runs/{run_id}/draft/deadline`，15 秒路由预算、1 KiB 正文，只接受原 Token 和下列字段：

```json
{"due_at_unix_ms":1791097200123,"expected_revision":"2"}
```

时间必须是显式 JSON 整数，范围 `0..253402300799999`；0 明确清除，缺失/null/数字字符串/小数/越界拒绝。版本为规范正十进制字符串。成功返回完整待确认草稿，时间与请求相同，版本为原值或安全加一；过时/冻结返回409，异常成功结果502。共享草稿输出对所有读写及回帖均检查截止时间范围，Gateway 不计算日期或判定成员权限。

Confirm 追加 optional 数字 `expected_due_at_unix_ms`；非零时间由 Agent 要求显式审查，缺失拒绝，零值旧草稿兼容缺失，新页面始终提交包括0。字段存在时结果必须一致，冻结重试仍用原值/版本/Task键。旧客户端不能只提交新版本盲确认已设置的截止时间。

草稿页面独立显示已保存时间，日期输入明确按 `Asia/Shanghai` 转换，保留秒/毫秒；人工 Task 表单继续原浏览器本地时间规则。空输入只改页面，须明确保存后才清除；未保存时间、文字或负责人阻止确认，冲突保留输入要求Load。缺时间字段的旧响应只读，不能自动补零；冻结和成功不可编辑。历史不存在/重复本地时间不接受新输入；未编辑已保存的精确UTC值仍可审查/重放。人工批次没有模型时间提取接线；其后的生成参考前置见下节。[共同契约](../docs/deadline-collaboration-contract.md)、[人工审查](../docs/agent-deadline-design.md#7-人工截止时间闭环审查)。

### 草稿生成的固定指令参考（2026-10-03）

现有生成POST正文支持optional数字`instruction_reference_unix_ms`，例如：

```json
{"instruction":"明天 15:30 完成缓存修复","instruction_reference_unix_ms":1791039599123}
```

缺失继续兼容旧请求，存在须JSON正整数`1..253402300799999`，null/0/字符串/小数/越界拒绝。Gateway不补当前时刻，原Token、请求键和范围转发Agent。新参考加入Agent请求指纹，同键变参考409；成功重放返回原run_id，不重新生成。响应仍仅run_id；参考不是服务端审计或授权时间。新页面需同版Gateway/Agent。

页面首次实际请求前固定设备时刻，显示当前请求的上海/UTC时间供核对，失败重试保留键/参考；变身份、范围、指令或显式新键属于新请求。本页已知旧键可复用原参考，刷新后未知手输键不猜参考，要求New key或凭Run ID读取。读取不显示伪造的持久参考。业务自动提取/填日期、解析元数据持久保存及读回下一批接线；[全部文件和验证](../docs/deadline-reference-review.md)。
