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
7 root验证实际HTML历史接线/同源嵌入和schema一致性。
8 root定向及全仓Go/相关与全页面Node回归。
9 root决策/进度/协作/验收/部署和全部实际文件审查。

| 角色 | 绝对目录和分支 | 唯一允许修改/新增 |
| --- | --- | --- |
| A IM | D:/zy/GoLang/go-im/.worktrees/assignee-backend；codex/stage7-group-unread-im | rpc/im/team_group_unread.go、rpc/im/team_group_unread_test.go |
| B Gateway | D:/zy/GoLang/go-im/.worktrees/assignee-gateway；codex/stage7-group-unread-gateway | api/team_group_unread.go、api/team_group_unread_test.go |
| C 原生页 | D:/zy/GoLang/go-im/.worktrees/assignee-ui；codex/stage7-group-unread-page | examples/team-group-unread.js、examples/team-group-unread.test.cjs |

共同起点提交后由root同步三个worktree。执行agent不得改协议/生成/迁移/依赖/共同文件/HTML，禁止test/build/Git写/合main/push/部署；只编辑/gofmt/diffcheck并报告，root集中验证/提交。没有关键新选择可直接沿契约实现，确需改变权限/数据/界面确认口径先上报。

## 本批实现与审查

共同准备及最终三分支、全部实际文件、测试结果/未验收范围由root交付前补充。真实MySQL/模型/浏览器/容器及030迁移未执行，不删除生成文件，main/push/云端均由用户后续审查决定。
