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
