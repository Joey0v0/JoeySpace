# 阶段7：本人团队群未读共同契约

2026-10-06；基线2177d4c，整合codex/stage7-team-group-unread，main89e2a1e。沿用户已选A73逐消息凭据及A74当前资格，只交付当前团队群的个人未读查询/显式确认与原生历史页操作；单聊、会话目录、A16退出流程、Push和结构日志不夹带。本页契约准备不代表实现或030已执行。

## 1. IM数据与业务边界

- 030/init新增IM拥有的im_group_message_reads，以(user_id,group_id,message_id)为主键，read_at由数据库CURRENT_TIMESTAMP(6)赋首次时间，冲突重试不能更新该时间。零历史回填，离队不删本人凭据；群资格恢复后按当前范围重新计算。
- 每个调用从原Token派生本人，以现有CheckTeamGroupAccess查当前群/团队资格和精确群归属，查询后/写入提交后再次核权。跨服务撤权与数据库不承诺原子一致；提交后核权失败可能已保存记录，本人再读/显式重试，不能说失败就没写。
- 全部当前可读群历史计入统计，排除本人普通用户消息（sender_type 0或1且from_id等于本人），机器人即使from_id与本人数字相同也计入。以具体消息读取凭据排除，不能用最大消息ID/created_at自动越过晚入库消息。GET只读，不插阅读记录。
- Mark请求为1—100个正消息ID，去重并排序。先核权，短事务核对所有ID存在且to_id为指定群、chat_type=2；任一缺失/跨群/单聊均整批NotFound且不写，避免部分确认。仅为非本人普通消息创建凭据，自己的普通消息可以确认但不产生无用记录；已读项再确认保留原read_at。仅按本人键写，不能自报读者。
- 批量验证消息可用共享行锁保护消息范围，记录用ON DUPLICATE KEY no-op保证首次时间；不引入ORM替换/新序号/后台worker。事务成功后复核资格并查询当前未读数。并发新消息可能使读后数量增加，不将返回数当权限或固定快照。

## 2. RPC / HTTP

root统一生成IM的GetTeamGroupUnread及MarkTeamGroupMessagesRead，generated默认未实现保持拒绝。Get输入team_id/group_id，回显同一范围及非负int64 unread_count。Mark输入范围+message_ids，回显同一范围、排序去重后的全部请求ID（含本人普通消息）及非负count；不返回其他读者/内容/租约。

- GET `/api/v1/teams/:team_id/groups/:group_id/unread` 无查询参数；只转单条Bearer，3秒上限继承请求取消。
- POST `/api/v1/teams/:team_id/groups/:group_id/read` 同样无查询参数，32KiB/单JSON/拒未知字段；body仅`{"message_ids":["9007199254740993"]}`，1—100个int64正字符串，去重后转RPC；身份/群归属取路径和原Token。
- success: `{code:0,msg:"success",data:{team_id:"200",group_id:"300",unread_count:"7"}}`；Mark另含message_ids字符串数组，与本次请求去重集合完全相等。不把大ID/count转JS Number。
- RPC错误映射沿当前团队群入口：400 InvalidArgument、401 Unauthenticated、403 PermissionDenied、404 NotFound、503 Unavailable、504 DeadlineExceeded，其余502；固定消息，不输出原始错误/正文。nil回包、范围不匹配、负数/错误ID回显均502且无data。

## 3. 原生页面接口

root拥有chat.html/嵌入/路由，新增groupUnreadCount、btnGroupUnreadRefresh、btnGroupReadLoaded、groupUnreadStatus四个DOM节点。脚本固定同源`/demo/team-group-unread.js`；子agent实现独立模块和测试，不改HTML。

模块加载后设置globalThis.teamGroupUnreadPage，提供`loaded({token,teamID,groupID}, messageIDs)`和`invalidate()`。root仅在当前历史请求合法/范围未失效后传本次成功加载的ID（历史页最多20个）；此调用替换上一页待确认ID，不自动查询/标已读。收到WS或离线ACK没有新调用。

模块自行读取token/teamId/toUserId/chatType输入，绑定按钮及输入/选择变化；任何相关事件都使旧结果失效（含A→B→A），root在原身份失效/成功登录函数补invalidate，覆盖同Token重新登录。按钮忙时禁止重复点击，旧回包不能覆盖新范围；较晚读取的新历史页也不能被旧确认回包清空。

手动GET更新计数（字符串）；Mark仅发送捕获的本次已加载ID。成功且回包范围/集合合法才展示返回count并清本页待确认，普通错误保留本页ID并允许本人重试；结果不确定提示可刷新计数或再次确认，不自动重试/自动ACK。401/403清计数/待确认/迟到响应，重新登录或范围输入变化后才能恢复，用户不能用重复点击保留撤销范围数据。不输出Token/正文/原错，以textContent呈现。

## 4. 九步与三个worktree

1 root核对计划/历史入口/已选规则。
2 root协议与生成代码、模型/030/init、共同路由/HTML接线/占位及契约，验证可编译，提交共同起点。
3 A实现IM查询/批量确认及本人/当前资格/事务/幂等测试。
4 B实现Gateway解析/回包/错误合同及HTTP→TCP RPC替身测试。
5 C实现原生模块、范围世代/显式操作和Node测试。
6 root审查保存并整合三分支，修实际问题。
7 root验证实际HTML历史接线/同源嵌入和schema一致性；运行入口发现旧Makefile单文件命令漏辅助文件，修正为按包运行。
8 root定向及全仓Go/相关与全页面Node回归。
9 root决策/进度/协作/验收/部署和全部实际文件审查。

| 角色 | 绝对目录和分支 | 唯一允许修改/新增 |
| --- | --- | --- |
| A IM | D:/zy/GoLang/go-im/.worktrees/assignee-backend；codex/stage7-group-unread-im | rpc/im/team_group_unread.go、rpc/im/team_group_unread_test.go |
| B Gateway | D:/zy/GoLang/go-im/.worktrees/assignee-gateway；codex/stage7-group-unread-gateway | api/team_group_unread.go、api/team_group_unread_test.go |
| C 原生页 | D:/zy/GoLang/go-im/.worktrees/assignee-ui；codex/stage7-group-unread-page | examples/team-group-unread.js、examples/team-group-unread.test.cjs |

共同起点提交后由root同步三个worktree。执行agent不得改协议/生成/迁移/依赖/共同文件/HTML，禁止test/build/Git写/合main/push/部署；只编辑/gofmt/diffcheck并报告，root集中验证/提交。没有关键新选择可直接沿契约实现，确需改变权限/数据/界面确认口径先上报。

## 本批实现与审查

共同7b5f062；root审查/定向验证后保存A IM59216a1、B Gateway04d9edc、C页面506a78e，无冲突合入codex/stage7-team-group-unread。执行agent各只改两份允许文件，没有测试/build/Git写或自行合main；root补HTML接线、同源嵌入/完整页面组合、测试修正及全部文档。三个worktree干净保留，main89e2a1e未变。

九步实际结果：先审查A73与旧历史；共同协议/生成/模型/DDL和拒绝占位；三个独立实现；root审查/定向/保存/整合；HTML/DDL/运行入口检查；Go/Node集中回归；最终决策/进度/协作/验收/部署记录。root没有执行真实迁移、部署或模型，不删除生成文件。

调用链：页面手动GET/POST → Gateway校验路径/具体字符串ID → IM原Token派生本人 → 群资格和User当前团队资格 → 只读COUNT或短事务整批验证/个人凭据 → 再核权及安全范围回显 → 页面接受当前世代结果。读取历史、WS到达、离线ACK不写凭据，UI只确认本次历史页。正常本人消息不计，机器人同数字发送者仍计。read_at靠数据库初值，重复写唯一键no-op；提交后失败可已保存，页面只提示本人查询/重试。

root另补成功新建、选择群和当前范围成功入群后invalidate，防止程序修改输入未触发DOM事件而残留旧计数/阻断；已拒绝的历史401/403清目标，迟到历史不能设置新目标。Get/Mark大数始终字符串，脚本仅textContent，不增加自动查询或自动ACK。

全部实际修改文件（27个）：

| 文件 | 目的 |
| --- | --- |
| [IM协议](../rpc/im/im.proto) | 独立只读统计与具体ID确认 |
| [生成消息](../rpc/im/pb/im.pb.go) | root正常生成新字段，未删除文件 |
| [生成RPC](../rpc/im/pb/im_grpc.pb.go) | 新Client/Server方法与描述符 |
| [凭据模型](../internal/model/group_message_read.go) | 本人/群/消息唯一键、数据库首次时间只读 |
| [030迁移](../deploy/mysql/migrations/030_im_group_message_reads.sql) | 已有库新增表，零回填 |
| [初始化](../deploy/mysql/init.sql) | 新库同一表定义 |
| [IM实现](../rpc/im/team_group_unread.go) | 当前资格、逐消息排除、事务确认/幂等 |
| [IM测试](../rpc/im/team_group_unread_test.go) | 12函数含SQL/User替身及实际TCP RPC |
| [Gateway实现](../api/team_group_unread.go) | 有界解析、原Token、精确范围/集合回显 |
| [Gateway测试](../api/team_group_unread_test.go) | 7函数含解析/取消及真实HTTP→TCP替身 |
| [Gateway路由](../api/main.go) | 两业务路由与固定同源脚本 |
| [嵌入测试](../api/chat_demo_test.go) | 页面DOM与嵌入脚本检查 |
| [嵌入声明](../examples/chat.go) | 新脚本打包进入Gateway |
| [页面接线](../examples/chat.html) | 控件、合法历史ID、身份/群变化失效 |
| [未读模块](../examples/team-group-unread.js) | 手动操作、世代/阻断/重试/字符串校验 |
| [模块测试](../examples/team-group-unread.test.cjs) | 16项VM/DOM/fetch场景 |
| [旧页面组合](../examples/chat.test.cjs) | 六项新增实际脚本/历史/范围接线验证 |
| [多草稿布局测试](../examples/multi-draft-view.test.cjs) | 只校验自身脚本顺序，兼容合法新模块 |
| [提醒布局测试](../examples/task-notifications-view.test.cjs) | 保留控制器/展示顺序，不锁定全页末尾 |
| [提醒完整页面测试](../examples/task-notifications-realtime.test.cjs) | 新六脚本真实装载，保持聊天/通知回归 |
| [Makefile](../Makefile) | 三服务go run按包编译，避免遗漏拆出的辅助文件 |
| [本契约](stage7-team-group-unread-contract.md) | 共同起点、九步、精确边界与全部文件 |
| [架构决策](architecture-decisions.md) | 已选A73内实现取舍与代价 |
| [项目进度](project-plan.md) | 已验证能力及下一轮恢复/权限补项 |
| [协作记录](worktree-collaboration-plan.md) | 三worktree和root整合责任 |
| [阶段7验收](stage7-acceptance.md) | 新本地证据与真实验收待项 |
| [部署说明](../deploy/README.md) | 先030再升级IM/Gateway和手动使用方式 |

验证：共同Go检查通过；A定向12函数通过，B首轮截止/取消测试误用WithContext丢pathvar，root只修两处测试请求保留路径值后7函数通过；C16项通过。root新增六项HTML接线/实际模块组合，首次全页面因三份旧测试锁定全页五脚本或末尾失败，更新合法装载与自身相对顺序后最终全部325项Node通过；全仓`go test ./... -count=1 -timeout=90s`通过。030/init表定义静态一致；三项`go run ./cmd/{api,ws,push} -h`退出0，未初始化数据库/启动服务。没有新依赖。

边界：SQL/User及页面DOM/fetch仍替身，部分HTTP/TCP为实际本机连接，不是浏览器→真实MySQL的完整链。首次时间及迟到小ID是SQL结构/回放验证，不冒称真实数据库提交顺序已测。030未执行，真实MySQL/模型/浏览器/Compose/云未验收，无main合并/push。保留既有“刷新最新历史不改变旧分页游标”，若迟到低ID不在最新20条、旧分页已结束，需要重新开始历史遍历；下一轮补显式重遍历/恢复组合，不把该消息自动判已读。A16退出清理/Push资格、Agent/通知关联日志及原路线单聊未读仍在后续清单。
