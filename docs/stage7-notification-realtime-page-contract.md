# 阶段7：原生页面任务通知提醒

2026-10-05；基线30e90ee，main89e2a1e。本批沿原生页面/A69—A72，只接已有五脚本与chat.html，不改服务/数据库/协议/依赖/部署配置，不新增脚本或框架。七步：共同契约/提示DOM1、状态1、展示1、组合测试1、WebSocket接线1、集中验证1、文档1。root统一契约/HTML/接线/文档/Git；三个执行agent只编辑允许文件，不test/build/Git写/合main/部署。

## A：状态与请求协调

唯一目录D:/zy/GoLang/go-im/.worktrees/assignee-backend，分支codex/stage7-notification-hint-state。唯一允许examples/task-notifications.js、task-notifications.test.cjs。

保持原state结构和旧方法，新增公开只读realtime getter返回副本{hasUpdate:boolean,recoveryPending:boolean}，不含ID/Token/详情。新增receiveHint(message,connectionToken)：syncScope，只有connectionToken等于当前有效scope.token且message严格对象仅type/data、type=task_notification_changed、data仅version/notification_id/team_id、version整数1、两个ID正规范十进制字符串int64且team等于当前scope才接受；无效返回false、不请求/不改错误或列表、不自动已读。JSON原文由root只在WebSocket回调解析，重复键检测不由状态层重复实现，不声称可发现JSON.parse已覆盖的重复键。有效提示返回true，将hasUpdate置true；重复同ID只合并，不反复notify，已在当前列表的ID也不重复提示。去重集合每范围内最多128条，FIFO移除，不长期存储；换范围/reset同时清空，不能用最高ID假定此前较小ID均已读到。

首次/重新连接调用recover(connectionToken)，有效当前scope且同Token才查GET最新第一页，不要求群；无效不请求。已有loading（分页/读取/PUT）时只记一个recoveryPending，不并发抢列表；同范围当前操作结束后排队一次GET，不自动重试网络故障、不轮询、不设timer。同范围连续recover合并，换scope/reset/401403清掉旧恢复。GET成功覆盖当前范围权威列表，不据提示内容拼接通知。普通收到提示不自动刷新。

提示修订用于处理并发：第一页请求开始捕获已收到提示序号，只有完整校验成功且请求期间没有新提示才清hasUpdate；分页或PUT成功不得清提示，失败/无效结果保持提示。401403清旧列表与提示/恢复，并阻止延迟旧提示再点亮，直到reset/换scope或一次成功授权GET；不改变原安全固定错误。每finally只当前generation解锁/通知/推进恢复，旧响应不能释放新范围busy或启动旧恢复。成功GET期间若又收到提示仍保留；128限制仅控制内存，窗口外重复可能再次提醒，不承诺全球去重。recoveryPending不是本人通知未读数。

## B：提示展示及身份输入

唯一目录D:/zy/GoLang/go-im/.worktrees/assignee-gateway，分支codex/stage7-notification-hint-view。唯一允许examples/task-notifications-view.js、task-notifications-view.test.cjs。

沿旧控制器/列表/按钮，在root已加的taskNotificationsRealtimeStatus只用textContent显示固定提示，hasUpdate为“有新的任务通知，请刷新通知。”，recoveryPending为“连接已恢复，将在当前操作结束后查询通知。”，其他为空；两者可合并一句，不回显任意事件字段。原status/分页/已读按钮仍同state.loading互斥，无innerHTML/本地Token持久化/轮询。Token input时在既有syncScope前调用globalThis.invalidateTaskNotificationConnection（存在时）以使旧socket失效，团队input不失效同账号socket；不主动新增WS或联网。换团队仍本人手动刷新或有效Team后重新连接，避免每次输入数字触发查询。

沿原测试夹具验证默认无WS/无自动读、收到提醒只亮提示不改列表或已读、合并重复、刷新/失败/期间新提示、scope切换/旧按钮、token invalidation钩子。不改HTML或root聊天测试，不把既有只读页面的历史测试全部重写。

## C：完整页面与WebSocket组合验证

唯一目录D:/zy/GoLang/go-im/.worktrees/assignee-ui，分支codex/stage7-notification-hint-tests。唯一新增examples/task-notifications-realtime.test.cjs。实际读取chat.html内联与全部五外部脚本，不用替身controller。用VM/DOM/WS/HTTP替身测试当前连接首次onopen→本人GET、重连重复读取、新提示不PUT/不拼详情、同ID/非法范围/大ID、refresh开始后新提示不会清、busy期间重连只排队一次、401403无旧列表、Token/团队换范围、旧socket的open/message/close不污染新连接、token A→B→A输入后旧socket无效、原离线chat pull/ACK调用仍走Gateway。只写测试，协议按上述和root下方接线，不额外要求生产出口/函数。构造DOM包含既有页面必要节点及WebSocket.OPEN/close/send，不能把“模拟socket”写成真浏览器。

## root：chat.html接线

doConnect捕获局部connection/token以及单独ws身份世代，替换旧socket使其失效；原回调仍供chat.test.cjs使用。globalThis.invalidateTaskNotificationConnection仅递增世代，token input与成功login调用；团队input不改变连接世代。当前连接判定ws===connection、身份世代相同、当前Token原值相同，onopen/onmessage不处理旧socket。onopen独立启动page.recover(token)，不等待离线chat pull才能恢复通知；不在提醒里带本人Token或增PUT。onmessage遇task_notification_changed类型时page.receiveHint(message,token)后return，即便无效也不进原raw RECV日志，避免展示伪造提醒详情。onclose只当前socket可清ws/setWsStatus，旧close不能断开新的连接；error同样限制当前socket。doDisconnect先使当前ws失效再close，保持原手动断开体验。页面没有自动建立WS或自动重连；用户点击连接的每次成功握手才触发恢复，未填写有效Team时不读，后来选Team仍可手动刷新。权限/连接世代是客户端范围隔离，真实内容仍由Gateway→Task当前核权，不声称实时撤权即自动清屏。

普通实现取舍：realtime单独于原state以保留旧分页/已读契约；128内存去重与布尔提示替代无界通知计数；请求修订与世代防并发旧结果；忙时串行排队一次替代并行抢列表。记录方案/备选/代价于architecture-decisions，未引入新的权限或跨服务方式。

## 本批实现与审查

实际完成七个小步骤，由三个独立worktree实现状态、展示和完整页面测试，root统一接线、验证及整合：

1. **共同契约与提示位置**：共同提交`bbc99fc`固定接口、文件边界及七步范围；chat.html增加独立提示节点，说明需要有效团队和手动连接，不改已有通知列表、分页或已读入口。
2. **状态和请求协调**：`a9f2413`增加只读realtime、严格范围校验、最多128项内存去重和提示修订。收到提醒只改变提示，不拼入详情、不请求已读；第一页读取期间的新提醒仍保留。401/403清旧列表并暂停迟到提醒，换账号/团队及reset让旧响应失效。状态测试46项通过。
3. **固定提示展示**：`bc1ea5c`用textContent展示“有新的任务通知，请刷新通知”及等待恢复查询提示；保留原列表与按钮，Token输入调用身份失效钩子。root先把状态分支快进提供给展示worktree，再测试实际两模块；展示测试32项通过。
4. **完整页面组合测试**：`8464d04`新增18项测试，真实加载chat.html内联和五份生产脚本，用VM/DOM/HTTP/WebSocket替身验证首次/重连查询、忙时排队、提示合并、权限拒绝、换范围及旧连接隔离。root先把已整合的生产代码快进提供给测试worktree，测试未替换生产controller。
5. **WebSocket实际页面接线**：root提交`d328ad8`，onopen独立启动通知恢复查询，原聊天离线拉取保持可运行；用连接对象、Token及身份世代限制旧回调，Token A→B→A或同Token重新登录也能失效旧socket，旧close不能清新连接。识别出的任务提醒不落入聊天原文日志；旧聊天测试及新增连接回归共155项通过。
6. **集中回归**：全部页面`node --test`共303项通过；`go test ./api -count=1`通过，确认Gateway原有固定嵌入和路由无需新增脚本或Go改动。该批没有后端Go实现变更，没有重复上一批全仓Go及Linux编译，也不把本次API测试说成全仓测试。
7. **审查和进度记录**：更新本契约、项目计划、架构取舍、协作记录及部署说明。三个执行分支已无冲突整合到`codex/stage7-notification-realtime-page`，三个worktree干净保留；root集中执行测试和Git写入，执行agent未提交、合main或部署。

当前调用链为：WS最小提醒→当前连接/本人/团队校验→固定更新提示→本人刷新→Gateway→Task当前权限查询。首次或手动重连成功且已填写有效团队时，页面直接查询最新一页；若正在分页、刷新或标已读，只排队一次恢复GET，当前操作结束后串行读取。恢复会回到最新页，不自动标已读；普通新提示不自动刷新，后续填写团队仍需本人刷新或重连。提醒不是通知详情、未读计数或浏览器送达凭证。

内存去重只有128项窗口，被移出的旧重复可能再次亮提示。收到权限拒绝后会清理与阻断旧提示，但不承诺后端撤权瞬间自动清屏，权限仍以Gateway→Task查询为准。没有自动建立WebSocket、自动重连、轮询或失败自动重试；网络失败留给本人刷新。原聊天实时消息去重、离线拉取和显式ACK在组合测试中通过。

**未验证部分**：测试使用浏览器相关替身，未运行真实浏览器、真实MySQL/Redis/Kafka/User部署链或证书挂载，也未执行027—029迁移、Compose、云端部署或真实模型调用。Task/Push/WS开关仍需最终按部署说明显式开启。本批未合入main、未push；main仍`89e2a1e`。下一批围绕通知全链路组合与故障恢复、阶段7验收清单推进，真实环境按用户决定留最终统一验收。

全部实际修改文件（相对基线`30e90ee`共12份）：

| 文件 | 本批用途 |
| --- | --- |
| [chat.html](D:/zy/GoLang/go-im/examples/chat.html) | 提示位置、局部连接回调与恢复入口 |
| [chat.test.cjs](D:/zy/GoLang/go-im/examples/chat.test.cjs) | 原聊天及连接隔离回归 |
| [task-notifications.js](D:/zy/GoLang/go-im/examples/task-notifications.js) | 提醒状态、范围校验与串行恢复 |
| [task-notifications.test.cjs](D:/zy/GoLang/go-im/examples/task-notifications.test.cjs) | 状态和并发回归 |
| [task-notifications-view.js](D:/zy/GoLang/go-im/examples/task-notifications-view.js) | 固定提示展示与Token输入钩子 |
| [task-notifications-view.test.cjs](D:/zy/GoLang/go-im/examples/task-notifications-view.test.cjs) | 提示展示和输入回归 |
| [task-notifications-realtime.test.cjs](D:/zy/GoLang/go-im/examples/task-notifications-realtime.test.cjs) | 完整页面与WS/HTTP组合验证 |
| [本批契约](D:/zy/GoLang/go-im/docs/stage7-notification-realtime-page-contract.md) | 共同接口、七步结果及全部文件定位 |
| [项目计划](D:/zy/GoLang/go-im/docs/project-plan.md) | 当前成果、下一步及验证边界 |
| [架构决策](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | 既定方案内的选择、备选、理由与代价 |
| [worktree协作记录](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三执行分支、root整合与测试责任 |
| [部署说明](D:/zy/GoLang/go-im/deploy/README.md) | 页面使用方式及最终环境验收边界 |
