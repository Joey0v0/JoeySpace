# 前端 F2 本批审查清单（2026-10-09）

集成分支：`codex/frontend-f2-navigation`，从 F1 `e75c322` 开始；`main` 未改，未推送或部署。用户确认 F06—F08 后完成八任务批次，范围见[设计](frontend-f2-navigation-design.md)、[接口契约](frontend-f2-api-contract.md)与[实施计划](superpowers/plans/2026-10-09-frontend-f2-navigation.md)。

## 逐项结果

| 步骤 | 实际改动与调用链 | 验证与限制 |
| --- | --- | --- |
| 1 共同协议 | User 本人团队/显示名、IM 群 `joined`/单群/持久私聊目录 RPC；主 agent 生成 pb，Gateway 旧替身补兼容 | User/IM/API 旧测试通过；IM proto 生成路径曾触发描述符编译错误，按原路径重生成后通过 |
| 2 User | Token → `GetMyInfo` → active `team_members` 分页；有限启用成员显示名 | 定向与全仓 Go 通过；真实 MySQL 未验 |
| 3 IM 群 | TeamMember 资格 → 群目录/单条 → 当前群成员与代际 fence → 返回前重验团队 | 群定向测试及新增撤权竞争测试通过；真实 MySQL 未验 |
| 4 IM 私聊 | Token → 双向持久 `messages` → 快照上界与每 peer 最新消息分页/单条 | 定向 Go 通过；真实 SQL 插入并发行为与 EXPLAIN 未验，未新增索引 |
| 5 Gateway | Bearer → User/IM RPC → 字符串 ID 的 HTTP；仅 IM 证实 peer 后调用 User 显示名 | API 全包、本机 HTTP→实际 TCP gRPC 组合通过；SQL 仍为替身 |
| 6 Vue 登录 | 登录/注册 → 本人资料复核 → `sessionStorage` 单标签身份；401 清理、503/504 重试 | 前端单测、构建与 mock Gateway 浏览器登录/刷新通过 |
| 7 Vue 导航 | 团队/群和私聊分页、单条深链接复核、显式入群重查；无假未读/@我 | 33 项前端业务测试、mock Gateway 浏览器主流程通过；真实服务双账号未验 |
| 8 集成 | 三个执行 worktree 提交按 User→IM→Vue 合入本分支，独立逐项和全分支代码审查 | `go test ./... -count=1`、`npm test`、`npm run build` 通过；Chrome 1280×720、1440×900、1920×1080 无横向溢出。headless CDP 键盘事件未送达焦点控件，Enter 激活未验收 |

F2 已具备本地可运行的真实导航代码；完整消息阅读、发送、未读/@我、主群配置、任务数据属于后续批次。真实 MySQL、索引计划、两账号真实服务整链及云端部署仍需单独验收，不据此标注为已通过。

## 全部修改文件

下表相对于 F1 提交 `e75c322`，包含本审查文件自身。点击文件名可直接定位；生成代码也逐项列出。

| 文件 | 归属 |
| --- | --- |
| [.gitignore](../.gitignore) | 共同配置 |
| [frontend_f2_flow_test.go](../api/frontend_f2_flow_test.go) | Gateway |
| [frontend_f2_navigation.go](../api/frontend_f2_navigation.go) | Gateway |
| [frontend_f2_navigation_test.go](../api/frontend_f2_navigation_test.go) | Gateway |
| [handler_test.go](../api/handler_test.go) | Gateway |
| [main.go](../api/main.go) | Gateway |
| [team_group_list.go](../api/team_group_list.go) | Gateway |
| [team_group_list_test.go](../api/team_group_list_test.go) | Gateway |
| [architecture-decisions.md](../docs/architecture-decisions.md) | 文档 |
| [frontend-f2-api-contract.md](../docs/frontend-f2-api-contract.md) | 文档 |
| [frontend-f2-navigation-design.md](../docs/frontend-f2-navigation-design.md) | 文档 |
| [project-plan.md](../docs/project-plan.md) | 文档 |
| [2026-10-09-frontend-f2-navigation.md](../docs/superpowers/plans/2026-10-09-frontend-f2-navigation.md) | 文档 |
| [worktree-collaboration-plan.md](../docs/worktree-collaboration-plan.md) | 文档 |
| [package.json](../frontend/package.json) | Vue |
| [App.vue](../frontend/src/App.vue) | Vue |
| [TaskPreview.vue](../frontend/src/TaskPreview.vue) | Vue |
| [client.test.ts](../frontend/src/api/client.test.ts) | Vue |
| [client.ts](../frontend/src/api/client.ts) | Vue |
| [LoginPage.vue](../frontend/src/auth/LoginPage.vue) | Vue |
| [session.test.ts](../frontend/src/auth/session.test.ts) | Vue |
| [session.ts](../frontend/src/auth/session.ts) | Vue |
| [verification.test.ts](../frontend/src/auth/verification.test.ts) | Vue |
| [verification.ts](../frontend/src/auth/verification.ts) | Vue |
| [ConversationList.vue](../frontend/src/messages/ConversationList.vue) | Vue |
| [ConversationView.vue](../frontend/src/messages/ConversationView.vue) | Vue |
| [MessagesPage.vue](../frontend/src/messages/MessagesPage.vue) | Vue |
| [UnreadOverview.vue](../frontend/src/messages/UnreadOverview.vue) | Vue |
| [directory.test.ts](../frontend/src/messages/directory.test.ts) | Vue |
| [directory.ts](../frontend/src/messages/directory.ts) | Vue |
| [router.ts](../frontend/src/router.ts) | Vue |
| [style.css](../frontend/src/style.css) | Vue |
| [vite.config.ts](../frontend/vite.config.ts) | Vue |
| [direct_conversation_detail.go](../rpc/im/direct_conversation_detail.go) | IM |
| [direct_conversation_detail_test.go](../rpc/im/direct_conversation_detail_test.go) | IM |
| [direct_conversation_list.go](../rpc/im/direct_conversation_list.go) | IM |
| [direct_conversation_list_test.go](../rpc/im/direct_conversation_list_test.go) | IM |
| [im.proto](../rpc/im/im.proto) | IM |
| [im.pb.go](../rpc/im/pb/im.pb.go) | IM |
| [im_grpc.pb.go](../rpc/im/pb/im_grpc.pb.go) | IM |
| [team_group_detail.go](../rpc/im/team_group_detail.go) | IM |
| [team_group_detail_test.go](../rpc/im/team_group_detail_test.go) | IM |
| [team_group_list.go](../rpc/im/team_group_list.go) | IM |
| [team_group_list_test.go](../rpc/im/team_group_list_test.go) | IM |
| [conversation_display_names.go](../rpc/user/conversation_display_names.go) | User |
| [conversation_display_names_test.go](../rpc/user/conversation_display_names_test.go) | User |
| [my_team_list.go](../rpc/user/my_team_list.go) | User |
| [my_team_list_test.go](../rpc/user/my_team_list_test.go) | User |
| [user.pb.go](../rpc/user/pb/user.pb.go) | User |
| [user_grpc.pb.go](../rpc/user/pb/user_grpc.pb.go) | User |
| [user.proto](../rpc/user/user.proto) | User |
| [frontend-f2-review.md](../docs/frontend-f2-review.md) | 文档 |
