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
