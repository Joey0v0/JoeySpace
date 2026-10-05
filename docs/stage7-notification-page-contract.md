# 阶段7：本人任务通知页面协作契约

日期：2026-10-05。基线：已通过定向/全仓Go回归的通知查询批次已合入本地main `b8ef5eb`。本批沿既定原生页面、A65—A67，只接本人通知手动读取，不新增权限、服务、协议、迁移或依赖。

## 页面与调用

页面在团队任务面板之后提供独立“我的任务状态通知”，共用已有Token和Team ID，不依赖所选群。按钮为“刷新通知”“加载更早通知”，状态区区分尚未读取、加载中、空列表、分页结束和错误。每项仅用纯文字显示任务ID、操作者ID、原/新状态和通知时间（Asia/Shanghai明确标识）；这代表持久状态变化记录，不标已读或实时送达，不推断当前任务状态，不读取任务标题或成员名字。

调用现有 `GET /api/v1/teams/{team_id}/task-notifications?limit=20&before_notification_id={cursor}`，原Bearer转发。只在本人点击时调用，不自动轮询、不新增WS事件。全部ID保留规范正十进制int64字符串（零只用于终止游标）；状态0/1/2且必须改变，UTC毫秒为安全整数正值且不超过253402300799999。整页校验后才修改列表/游标，按BigInt比较ID严格倒序且小于请求游标，只有20项且下一游标等于最后展示ID才可继续。空/末页下一游标为`"0"`。

## 状态模块接口

独立同源脚本 `/demo/task-notifications.js` 导出 `globalThis.TaskNotificationsController` 类，无自动初始化或DOM操作。构造参数 `{fetch,getScope,onChange}`；`getScope()`返回`{token,teamID}`或null，仅token非空和规范正int64团队ID有效。公开只读使用的`state`对象包含`items`数组、`cursor`字符串、`loaded`/`loading`布尔值、`error`固定安全文字。初始items=[]/cursor='0'/loaded=false/loading=false/error='';不向state泄露Token。方法：`syncScope()`比较当前身份/团队并在变化时清空、递增世代；`reset()`无条件清空并递增世代；异步`refresh()`从0替换读取；异步`loadMore()`仅loaded且cursor非0时继续；方法不把原始错误/Token暴露到页面，不抛未处理异常，状态变更调onChange。

单一范围内正在加载时重复点击不发第二请求；切换范围后新范围可立即读取，旧fetch/json/错误/finally不能覆盖新状态。同范围Refresh清空旧列表，失败显示未读取/可刷新；普通下一页网络/服务错误保留已读列表和原游标以便重试，但401/403清空全部已展示记录、loaded=false/cursor0且须刷新。陈旧上下文的错误不显示。Token/团队input事件调用syncScope；登录程序写Token（即使同值）须显式reset，防止旧身份请求覆盖。仅在fetch前后比较当前值不足以识别A→B→A，输入事件必须使世代失效。不在localStorage、日志或错误文字保存Token。

## 展示接线

独立脚本 `/demo/task-notifications-view.js` 自动初始化`globalThis.taskNotificationsPage`，实例化上述控制器、只读token/teamId两字段。固定面板元素：`taskNotificationsPanel`、`btnRefreshTaskNotifications`、`btnMoreTaskNotifications`、`taskNotificationsStatus`、`taskNotificationsList`。使用textContent/createElement/replaceChildren渲染，不用innerHTML；按钮加载时禁用，更多仅loaded且非零cursor可用。Token/teamId input事件同步清空；chat.html登录成功替换Token后，若控制器已初始化调用reset。脚本顺序在已有multi-draft脚本之后先controller后view，固定同源URL。

## Worktree边界和验收

| 子任务 | 绝对工作目录 | 分支 | 允许文件 |
| --- | --- | --- | --- |
| 分页状态 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-notification-page-state | 新examples/task-notifications.js、task-notifications.test.cjs |
| 页面面板与展示 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-notification-page-view | examples/chat.html，新examples/task-notifications-view.js、task-notifications-view.test.cjs |
| HTTP/TCP传输验证 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-notification-page-transport | 新api/task_notifications_transport_test.go |
| 主agent | D:/zy/GoLang/go-im | codex/stage7-notification-page | examples/chat.go、api/main.go、api/chat_demo_test.go、共同文档/审查/Git整合/集中测试 |

三个子agent仅改允许文件、格式化和检查差异；不运行测试/build、不提交、不合main、不推送部署。状态模块先实现，页面按共同接口独立实现，集成后主agent测试真实两脚本加chat页面与DOM/HTTP替身；运输测试用实际HTTP与TCP gRPC生成客户端/Task服务替身，不假称生产MySQL/User权限链通过。主agent统一embed和生产固定路由，审查后逐分支提交、合入本批整合分支，运行相关Node和全仓Go回归。

五步为整批范围，无须凑满九步。真实027/MySQL/浏览器/容器/云验收仍待最终统一执行。

## 本批实现与审查

本批五步目的和实际结果：

1. **保存共同基线**：上批查询快进合入本地main `b8ef5eb`，本批另建整合分支并保存共同页面契约 `b63ecf4`。解决并行实现时控制器/展示/HTTP预期不一致；未推送远程。
2. **通知分页状态**：状态agent新增控制器和业务测试，严格校验整页后提交，20条倒序/字符串大ID，输入事件和reset递增世代；旧fetch/json成功、错误或finally均不能覆盖当前加载。普通下一页失败保留列表/游标，401/403清空。主agent定向17项通过后保存 `13f8dde`。
3. **页面面板**：页面agent增加纯文字通知列表、手动刷新/加载更早、上海时间和加载/空/末页提示，只复用Token/团队不需要群。addEventListener保留原inline事件，登录成功写Token后reset含同Token。执行分支 `def7c12`；主agent整合后统一Team ID trim，并把测试提升为实际chat内联和全部五外部脚本共同运行，最终15项组合通过。
4. **HTTP→TCP gRPC**：传输agent新增真实HTTP server/client→生产Gateway处理器→生成Task客户端→本机TCP gRPC Task替身组合。核对原Token/team/cursor/limit、大ID字符串、满/末/空页、权限403不泄露私有错误，以及自报接收人查询不触发RPC。主agent定向通过，保存 `90d058e`。
5. **Gateway嵌入与集中回归**：主agent为两脚本添加go:embed和固定同源GET路由，静态资源测试核对正文/类型/no-store；旧多项页面测试仅更新脚本清单断言，旧业务不改。三分支无冲突合入 `codex/stage7-notification-page`，全页面236项Node、全仓Go通过；更新开发/API/部署文档和既定选型实施记录。main仍为`b8ef5eb`，本批待审查。

调用链：用户点击→通知控制器固定当前Token/团队/游标→同源Gateway GET→Task RPC→User资格核对→Task本人通知/操作查询→资格复核→Gateway字符串响应→当前请求世代整页校验→纯文字页面展示。服务端查询及写入沿上一批，本批没有改它们。

实际验证：`node --test examples/task-notifications.test.cjs` 17项通过，初次双通知脚本页面组合14项通过；主agent补Team ID规范化和完整五脚本共同加载后，组合15项通过。首次完整组合测试发现测试VM缺少旧草稿模块使用的crypto，补标准WebCrypto到测试环境，无需改旧产品模块。最终 `node --test --test-reporter=tap examples/*.test.cjs` **236/236**通过；`go test ./api -run '^TestTaskNotificationsHTTPOverTCPGRPC$' -count=1`及整合后`go test ./... -count=1`通过。代码测试和差异检查已完成。

未验证：真实浏览器布局/事件、真实HTTP→生产Task/User→MySQL整链、027迁移、MySQL时间和索引计划、跨进程并发撤权、容器/云验收。Node使用实际页面脚本但DOM/HTTP为替身；Go传输组合真实走HTTP和TCP gRPC但Task服务是替身。没有部署或调用真实模型，也未提供未读数、已读操作或实时提醒。下一步讨论已读/未读规则后再实施。

本批相对main实际修改16个文件，没有协议/生成代码、SQL迁移或依赖变动：

| 文件定位 | 实际修改 |
| --- | --- |
| [examples/chat.html](D:/zy/GoLang/go-im/examples/chat.html) | 通知面板、脚本标签、登录后reset |
| [examples/chat.go](D:/zy/GoLang/go-im/examples/chat.go) | 嵌入两份通知脚本 |
| [examples/task-notifications.js](D:/zy/GoLang/go-im/examples/task-notifications.js) | 独立分页/范围状态控制器 |
| [examples/task-notifications.test.cjs](D:/zy/GoLang/go-im/examples/task-notifications.test.cjs) | 17项控制器业务测试 |
| [examples/task-notifications-view.js](D:/zy/GoLang/go-im/examples/task-notifications-view.js) | 安全展示、上海时间、按钮和input接线 |
| [examples/task-notifications-view.test.cjs](D:/zy/GoLang/go-im/examples/task-notifications-view.test.cjs) | 15项完整页面/五脚本组合测试 |
| [examples/multi-draft-view.test.cjs](D:/zy/GoLang/go-im/examples/multi-draft-view.test.cjs) | 仅适配外部脚本清单顺序断言 |
| [api/main.go](D:/zy/GoLang/go-im/api/main.go) | 两个固定同源脚本GET路由 |
| [api/chat_demo_test.go](D:/zy/GoLang/go-im/api/chat_demo_test.go) | 通知嵌入静态资源校验 |
| [api/task_notifications_transport_test.go](D:/zy/GoLang/go-im/api/task_notifications_transport_test.go) | 实际HTTP/TCP gRPC传输组合测试 |
| [api/README.md](D:/zy/GoLang/go-im/api/README.md) | 通知面板使用和边界 |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md) | 嵌入页面更新方式和验收限制 |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | 既定A27/A65—A67下的选择/备选/代价 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 当前进度、main和下一步 |
| [docs/worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三个执行分支交付/整合记录 |
| [docs/stage7-notification-page-contract.md](D:/zy/GoLang/go-im/docs/stage7-notification-page-contract.md) | 共同接口、五步审查和全部文件 |
