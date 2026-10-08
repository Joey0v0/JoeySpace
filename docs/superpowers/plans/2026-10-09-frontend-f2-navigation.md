# JoeySpace 前端 F2 真实登录与会话导航 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 成员在桌面浏览器真实登录后，无须手填 ID 即可找到本人团队、团队群和有持久聊天记录的私聊对象，并在刷新、直接链接、退出和撤权时得到正确结果。

**Architecture:** 沿用现有 Bearer 登录；User 负责本人团队与有限显示名，IM 负责群资格与持久私聊目录，Gateway 组合 HTTP 响应，Vue 负责路由和状态。先交付真实会话导航，消息阅读、未读聚合和主群配置遵循独立的后续契约。

**Tech Stack:** Go、gRPC、MySQL、现有 HTTP Gateway；TypeScript、Vue 3、Vite、Vue Router；Go/Node 内置测试与本地 Chrome。

**Spec:** [F2 设计](../../frontend-f2-navigation-design.md)，实施前须经用户审查其中 3.1—3.3 的身份、服务数据边界和阶段范围。

## Global Constraints

- 保留 F1 的三栏消息框架和桌面优先体验；核心导航真实可用，不显示固定样例、假未读数或假 @我。
- 所有 HTTP ID 和游标用十进制字符串，前端不得转为 JavaScript Number；分页默认 20、上限 100。
- 目录发现与消息读取分别核权：看得见群不等于已加入；入群只能由成员显式操作。
- 仅从持久 `messages` 发现私聊对象；不从 `offline_messages` 推断，也不要求双方仍属于同一团队。
- 不新增公开的任意用户查询、Token 刷新、自动入群、主群标记、已读写入或跨类型未读聚合。
- 主 agent 独占 proto/生成代码、迁移、依赖、共享文档及集成；最多三个执行 agent 在从共同提交创建的独立 worktree 中修改指定非共享文件，不自行合并 main。
- 全批最多九个可单独审查的小任务；每个任务开始前说明目的、解决的问题和预计文件，结束记录实际文件、调用链、测试及未验证处。用户可提前审查。
- 保留现有工作和未提交修改；F2 从 F1 `e75c322` 起步，本批只在 `codex/frontend-f2-navigation` 集成，不自动合 main、推送或部署。

## Review Focus

1. 登录者切换账号而旧目录请求随后完成：不得把旧账号会话显示给新账号；Task 6、7 测试请求失效。
2. 团队退出或重新加入后群成员代际变化：群目录和单条查询都不得把旧成员资格显示为已加入；Task 3 测试。
3. 新消息在私聊目录翻页期间进入：快照内的旧会话不得重复或漏掉；Task 4 测试。
4. 只有陌生 peer ID 的直接链接：不得借显示名 RPC 泄露其资料；Task 4、5 测试。
5. 网关部分失败或 503/504：不得将未获取目录渲染为“没有会话”；Task 5、7 测试。

## 文件职责与协作基线

主 agent：`D:/zy/GoLang/JoeySpace/.worktrees/frontend-f2-navigation`，拥有 `rpc/user/user.proto`、`rpc/im/im.proto`、相应 `pb/*`、`api/*`、`docs/*`、`frontend/package*` 与最终集成。审查方案后先固定 proto 和共同提交，再由同一提交建立三个执行 worktree，并在共同文档记下各绝对目录、分支、允许文件。User agent 只写 `rpc/user/` 非 proto/pb 的实现与测试；IM agent 只写 `rpc/im/` 非 proto/pb 的实现与测试；Vue agent 只写 `frontend/src/` 与其测试。主 agent 逐任务审查、合入本批分支和统一验证。若隔离 worktree 无法创建，主 agent 在现有 F2 worktree 顺序执行，并记录原因；不越界修改原 F1/main。

推荐协议字段以设计文档第 4 节为准；主 agent 在 Task 1 将各 RPC 的确切 request/response 字段、错误码及 HTTP 映射固定在 proto 与同目录契约说明中。新增 RPC 不由浏览器直连。现有 `GET /api/v1/teams/:team_id/groups` 保持可发现全部群的语义。

## Task 1: 固定共享读契约与共同基线

**Files:** Modify `rpc/user/user.proto`、`rpc/im/im.proto`；regenerate `rpc/user/pb/user*.pb.go`、`rpc/im/pb/im*.pb.go`；Create `docs/frontend-f2-api-contract.md`；Modify `docs/architecture-decisions.md`、`docs/project-plan.md`、`docs/worktree-collaboration-plan.md`。Owner: 主 agent。

**Interfaces:** User 新增 `ListMyTeams`, `BatchGetConversationDisplayNames`；IM 的 `TeamGroup` 增 `joined`，新增 `GetTeamGroup`, `ListMyDirectConversations`, `GetMyDirectConversation`。团队、群和私聊列表的 ID/游标以 `int64` gRPC 传递、十进制字符串 HTTP 输出；私聊列表包含 `snapshot_upper_message_id` 与 `before_last_message_id`，单条查询按本人和 peer 核验。

- [ ] 审查用户确认的 F2 选型，先固定协议测试中的方法与字段要求；运行对应 `go test`，期望因新接口缺失而失败。
- [ ] 只追加兼容字段与 RPC，按仓库现有生成方式生成 pb；写契约说明，列请求范围、limit、空目录、403/404/503/504、无名对象及旧客户端兼容性。
- [ ] 运行 proto 生成差异检查和相关包编译；补新 RPC 的明确 `Unimplemented` 占位及旧测试替身声明，让共同基线可编译。占位必须返回未实现，不能提前开放成功路径；业务由 Task 2—4 实现。
- [ ] 把已确认的选项、备选、理由和代价记入架构记录；保存共同提交，检查三个执行目录在仓库内、从同一提交出发且各自干净。

## Task 2: User 本人团队与安全显示名

**Files:** Create `rpc/user/my_team_list.go`、`rpc/user/my_team_list_test.go`、`rpc/user/conversation_display_names.go`、`rpc/user/conversation_display_names_test.go`；Modify `rpc/user/server.go` only for RPC registration. Owner: User agent。

**Interfaces:** 消费 Task 1 的 `ListMyTeams` 和 `BatchGetConversationDisplayNames`。本人团队只取活动成员关系，按 `team_id > after_team_id ORDER BY team_id LIMIT limit+1`；名称读取最多 100 个去重正 ID，只返回昵称优先、用户名兜底的安全显示字段，不返回邮箱/状态或区分未知与停用账户。

- [ ] 先写测试：两账号隔离、离队中/已退出排除、分页边界和大 ID 原样返回；显示名测试含重复/未知/停用/超过 100 个 ID。
- [ ] 运行 `go test ./rpc/user/...`，确认新增测试按预期失败；实现最小 RPC 查询与现有认证/状态检查。
- [ ] 再运行 `go test ./rpc/user/...`，检查通过；提交仅本任务允许文件并向主 agent 报告调用链和 SQL 替身/真实库界限。

## Task 3: IM 群目录成员状态与直接定位

**Files:** Modify `rpc/im/team_group_list.go`、`rpc/im/team_group_list_test.go`、`rpc/im/server.go`；Create `rpc/im/team_group_detail.go`、`rpc/im/team_group_detail_test.go`。Owner: IM agent。

**Interfaces:** `ListTeamGroups` 沿现有团队目录增加 `joined`；`GetTeamGroup(team_id,group_id)` 返回相同群字段和当前 `joined`，无法由当前团队成员发现则 403/404，不能从列表位置推断主群。

- [ ] 先写群测试：活动团队成员可见未加入群且 `joined=false`；加入后为 true；离队、重新加入导致代际不匹配、关闭/删除、未知群和另一团队群均不能误标 true。
- [ ] 运行 `go test ./rpc/im/...`，确认新增测试失败；复用现有群访问与代际/关闭核验实现列表及单条读取，不触发加入或已读写入。
- [ ] 运行 `go test ./rpc/im/...`，确认通过；提交仅本任务允许文件，记录与消息阅读核权不同的边界。

## Task 4: IM 持久私聊目录

**Files:** Create `rpc/im/direct_conversation_list.go`、`rpc/im/direct_conversation_list_test.go`、`rpc/im/direct_conversation_detail.go`、`rpc/im/direct_conversation_detail_test.go`；Modify `rpc/im/server.go`。若查询计划确需新索引，主 agent 单独负责 `deploy/mysql/init.sql` 和新的 `deploy/mysql/migrations/` 文件，并先补契约审查。Owner: IM agent；迁移仅主 agent。

**Interfaces:** `ListMyDirectConversations` 从双向 `messages` 的直接消息按 peer 分组，取每个 peer 在 `id <= snapshot_upper_message_id` 下的 `MAX(id)`，再以最新 ID 倒序/`< before_last_message_id` 分页；第一页服务端确定快照上界。`GetMyDirectConversation(peer_id)` 只在本人和该 peer 至少有一条持久直接消息时返回。本人 ID 从现有认证上下文获取，不能由请求体自报。

- [ ] 先写测试：双向、重复消息去重、分页、新消息插入、空目录、仅离线投递记录、跨队历史、陌生 peer、自己/零/负 ID、大 ID。
- [ ] 运行 `go test ./rpc/im/...`，确认新增测试失败；实现最小查询和游标验证，保证失败不返回部分成功页。
- [ ] 在隔离测试库用代表性样本运行 `EXPLAIN`，记录现有索引、行数和访问路径；只有有证据显示不可接受时，主 agent 才审查并准备增量迁移。
- [ ] 运行 `go test ./rpc/im/...`，确认通过；提交本任务允许文件，说明 SQL 替身与真实 MySQL 的验证范围。

## Task 5: Gateway 本人目录 HTTP 接口

**Files:** Modify `api/main.go`、`api/team_group_list.go`、`api/team_group_list_test.go`；Create `api/my_teams.go`、`api/my_teams_test.go`、`api/team_group_detail.go`、`api/team_group_detail_test.go`、`api/direct_conversations.go`、`api/direct_conversations_test.go`。Owner: 主 agent。

**Interfaces:** 新增 `GET /api/v1/teams`、`GET /api/v1/teams/:team_id/groups/:group_id`、`GET /api/v1/me/direct-conversations`、`GET /api/v1/me/direct-conversations/:peer_id`；沿用现有群目录与显式入群路由。所有查询仅接受 Bearer；私聊名称仅对 IM 返回的 peer ID 调 User 批量 RPC，接口响应以字符串输出 ID/游标。

- [ ] 先写 HTTP 测试：Token 身份透传、分页和大 ID、群 `joined`、陌生 peer 不调用显示名 RPC、未知显示名中性兜底、401/403/404/503/504 映射与后端不完整回包。
- [ ] 运行 `go test ./api/...`，确认新增测试失败；实现 handler、路由与最小组合，不提供任意用户名称查询。
- [ ] 运行 `go test ./api/...`，确认通过；主 agent 审查服务端合入、保存精确文件，并记录 HTTP→RPC→MySQL 调用链。

## Task 6: Vue 登录、会话身份与本地 API 适配

**Files:** Create `frontend/src/auth/session.ts`、`frontend/src/auth/session.test.ts`、`frontend/src/auth/LoginPage.vue`、`frontend/src/api/client.ts`、`frontend/src/api/client.test.ts`；Modify `frontend/src/router.ts`、`frontend/src/App.vue`；主 agent Modify `frontend/vite.config.ts`、`frontend/package.json`。Owner: Vue agent；代理配置与测试脚本由主 agent 修改。

**Interfaces:** `session.ts` 提供 `restoreSession(): string | null`、`setSession(token: string): void`、`clearSession(): void`，仅内存及 `sessionStorage`；`client.ts` 统一 `code/msg/data`、Bearer、超时/网络错误和字符串 ID，不把 Token 放 URL。登录成功再 `GET /api/v1/user/info`，401 清理身份，资源 403 不登出。

- [ ] 先写会话/API 测试：刷新复核、退出清理、账号切换时旧请求失效、401 与 403 分流、Token 不进入 URL/日志；运行 `npm test`，确认新增测试失败。
- [ ] 实现登录页和状态模块，复用现有 `POST /api/v1/user/login` 与本人资料；主 agent 配 Vite 本地 `/api` 代理到现有 Gateway，并将 `npm test` 脚本明确列入 `model.test.ts`、`session.test.ts`、`client.test.ts`，Task 7 再加入 `directory.test.ts`。
- [ ] 运行 `npm test && npm run typecheck && npm run build`，确认通过；提交仅允许文件，由主 agent 单独保存代理配置。

## Task 7: Vue 真实会话导航

**Files:** Modify `frontend/src/messages/MessagesPage.vue`、`frontend/src/messages/ConversationList.vue`、`frontend/src/messages/ConversationView.vue`、`frontend/src/messages/UnreadOverview.vue`、`frontend/src/messages/model.ts`、`frontend/src/messages/model.test.ts`、`frontend/src/style.css`；Create `frontend/src/messages/directory.ts`、`frontend/src/messages/directory.test.ts`。Owner: Vue agent。

**Interfaces:** 目录适配 Task 5 HTTP 接口；团队/群与私聊分别分页，列表可搜索当前已加载项。群路由和私聊路由直接打开时调用单条接口复核；未加入群显示显式加入操作，加入成功后重查。当前内容区明确提示消息阅读后续接入，未读/@我不显示样例或虚假零值。

- [ ] 先写模型/目录测试：真实空页与错误区别、字符串大 ID、深链接 403/404、重复快切、账号切换、分页失败保留已加载项、加入后状态重查；运行 `npm test` 确认失败。
- [ ] 接入真实目录并替换 F1 样例业务渲染；保留三栏、键盘焦点、浏览器前进后退与“我的任务”入口。
- [ ] 运行 `npm test && npm run typecheck && npm run build`，确认通过；提交仅允许文件并记录浏览器尚待验证的交互。

## Task 8: 集成验证、审查与进度记录

**Files:** Modify `docs/project-plan.md`、`docs/worktree-collaboration-plan.md`、`docs/frontend-f2-navigation-design.md`；必要时新增精确集成测试文件 `api/frontend_f2_flow_test.go`。Owner: 主 agent。

**Interfaces:** 集成 Task 2—7 的受审提交，保持 main/F1 不变；只把实际通过的范围标为完成。

- [ ] 核查各提交和允许文件，解决冲突并运行 `git diff --check`、`go test ./...`、前端 `npm test`、`npm run typecheck`、`npm run build`。
- [ ] 在可用本地环境用两个隔离账号走登录→本人团队→群发现/显式加入→持久私聊目录→深链接→离队撤权→退出；用本地 Chrome 检查 1280×720、1440×900、1920×1080 与键盘操作。环境不可用时如实列出未验证环节，不把替身通过当真实 MySQL/浏览器通过。
- [ ] 依据 `superpowers:requesting-code-review` 做独立审查，修复实质问题后重新跑受影响验证；更新进度和全部改动文件定位，提交 F2 集成分支供用户集中审查，不自动合 main、推送或部署。
