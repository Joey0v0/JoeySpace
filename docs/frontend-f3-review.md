# F3 第一批真实聊天审查（2026-10-09）

基线：F2 `37c72cd`；集成分支：`codex/frontend-f3-chat`。用户已确认 F09—F11，本批按[八步计划](superpowers/plans/2026-10-09-frontend-f3-chat.md)仅交付第一批讨论链路；F10 权威跨会话总览与 F11 结构化普通提及留下一批。F2/main 没有合并、推送或部署动作。

## 八步结果与调用链

1. **共同契约。**主 agent 固定票据、Origin、现有历史/已读/离线与发送状态边界（`5a70dbf`）。WS HTTP 入口由 WS 服务自己持有，复用其 Redis，不让 Gateway 新增跨服务票据调用。
2. **历史读取。**Vue 执行分支从共同提交实现群/私聊最新页与更早页、字符串大 ID、旧请求隔离和滚动位置保持（`e6624a3`，集成 `940901a`）。浏览器 → Gateway → IM；当前群需已加入，私聊需本人可读。
3. **显式已读。**会话 `/unread` 显示权威计数；仅用户点击才把已加载收到的持久消息 ID 以每批最多 100 条提交 `/read`，成功后重查。机器人群消息即使数字发送者 ID 与本人相同也计入；超过 100 条可逐批前进；超时保留原 ID 批，复核后才显式同批重试（修复 `b92445f`，集成 `471a399`）。历史/实时/离线 ACK 均不会自动已读。
4. **一次性 WS 票据。**浏览器以 Bearer 向 WS 的 `POST /ws-ticket` 换 30 秒随机票据，原 JWT 只在短期服务端 Redis 记录；`GET /ws?ticket=` 原子消费，Origin 在消费前检查。旧 `?token=` 保留兼容、需显式允许跨端口页面 Origin。Vite 代理 `/ws-ticket` 与 `/ws`；Compose 提供来源配置。后端 `7ef2a83`，兼容测试 `9b99dcf`。
5. **发送与状态。**实时执行分支 `bb2be11`（集成 `040d354`）实现新票据重连、身份代际隔离、纯文本帧校验、同连接单条待 ACK、重复帧去重。页面为每条发送保存稳定 `msg_id` 和正文；ACK 显示“已受理”，收到持久实时帧或重查历史才显示“进入历史”。断线/超时标“待核对”，先查同 ID 历史；不确定结果仅在五分钟服务端去重窗口内由用户显式同 ID 重试，不自动换 ID。
6. **离线补拉。**WS 每次连上后请求 IM 离线列表；完整校验后先放当前账号内存缓存，再最多 1000 条一批 ACK。群/私聊当前消息按 `msg_id` 与实时合并；ACK 不改变已读。旧接口无分页，超过首批明确提示继续补拉，不声称已完整处理。离线查询/ACK 失败保留提示。整合 `52dd4fd`。
7. **桌面交互。**保留三栏和输入草稿，Enter 发送、Shift+Enter 换行、输入法组合期间不发送；断线可继续编辑但不可发送，底部滚动与新消息提示分开。群发送者名称优先使用现有团队成员目录，历史已离队者保留“成员 ID”说明。聊天正文和操作字号经截图检查调整；未读总览文案明确等待 F10/F11，不能显示假计数。整合 `52dd4fd`。
8. **集成验证。**主 agent 审查两个执行分支文件边界并修复机器人同 ID、100 条已读停滞、实时早于历史页时游标丢失、发送结果不确定时误换 ID、撤权后草稿残留等问题。独立审查发现持久消息先于 ACK 到达会留下待确认定时器；现已清理待确认状态并加入回归测试。全仓 Go 与统一前端检查通过；本机 Chrome 用 HTTP/WS 替身走登录、私聊历史、票据连接、发送入流、显式已读，在 1280×720、1440×900、1920×1080 均无横向溢出，截图人工查看。

## 验证结果与限制

- `go test ./... -count=1` 全仓通过（WS 后端提交后；后续只改前端/文档）。WS 实际 HTTP/握手测试覆盖签票、非法 Origin 拒绝且不耗票、消费后重放 401、旧 Token 白名单 Origin；仓储用替身，未接真实 Redis。Compose Redis 模板为 7 系列，`GETDEL` 实际运行仍未验证。
- 前端统一 `npm test` **53/53** 通过；`npm run build` 包含 `vue-tsc --noEmit`，通过。关键测试涵盖账号切换、历史分页、大 ID、100 条已读、回包不确定、离线 ACK 顺序、WS 去重/重连/超长帧及持久消息先于 ACK 的竞态。
- Chrome 本机与替身服务完成私聊核心流程及三种尺寸视觉核对；按钮点击已验，物理键盘 Enter 和中文输入法组合未做真实浏览器事件验收。群聊在历史/已读模型与 WS 发送契约测试中覆盖，未跑两账号真实群聊浏览器链。
- 未运行真实 MySQL、Redis、Kafka、Docker Compose 或云端 F3；生产同源反代和旧演示页公网 Origin 配置待 F6 验收。`/message/offline` 当前无分页，大积压只能分批 ACK/提示；非当前会话离线消息在 ACK 后依赖日后打开会话读取持久历史，F10 接入前缺少完整跨会话发现；后端正式分页契约未实现。F10/F11、任务与 AI 前端仍未完成，不能将 F3 全阶段标完成。

## 全部实际修改文件

后端与部署：[WS 入口](../cmd/ws/main.go)、[WS 服务与来源检查](../internal/ws/server.go)、[票据 Redis 仓储](../internal/repository/ws_ticket.go)、[WS 票据测试](../internal/ws/ticket_test.go)、[Compose](../deploy/docker-compose.yaml)、[部署指南](../deploy/README.md)。

前端工程与页面：[测试脚本](../frontend/package.json)、[Vite 代理](../frontend/vite.config.ts)、[消息页](../frontend/src/messages/MessagesPage.vue)、[会话视图](../frontend/src/messages/ConversationView.vue)、[会话栏](../frontend/src/messages/ConversationList.vue)、[总览过渡文案](../frontend/src/messages/UnreadOverview.vue)、[聊天样式](../frontend/src/style.css)。

前端逻辑与测试：[历史/已读模型](../frontend/src/messages/history.ts)、[历史/已读测试](../frontend/src/messages/history.test.ts)、[离线补拉](../frontend/src/messages/offline.ts)、[离线测试](../frontend/src/messages/offline.test.ts)、[实时客户端](../frontend/src/realtime/client.ts)、[实时客户端测试](../frontend/src/realtime/client.test.ts)。

共同文档：[F3 设计](frontend-f3-chat-design.md)、[首批 API 契约](frontend-f3-api-contract.md)、[实施计划](superpowers/plans/2026-10-09-frontend-f3-chat.md)、[架构选择](architecture-decisions.md)、[项目计划](project-plan.md)、[协作记录](worktree-collaboration-plan.md)、本审查文档。
