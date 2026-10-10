# F6 正式 Vue 入口第二批审查（2026-10-10）

## 阶段判断

正式 Vue 入口已在阿里云 `/opt/JoeySpace` 以同源 Nginx 和回环 `127.0.0.1:18083` 运行，浏览器需经 SSH 隧道访问。F6 任务 6—11 的已测业务链路均有真实浏览器通过样本；任务 9 早期 Ask 连续 503，另一条指令耗尽预算，模型稳定性尚未证实。跳过 409 已定向修复并复验。**F6 暂不标记为无条件完成，不合并 `main`，不推送，也不开放公网入口**。逐项证据见[验收清单](frontend-f6-acceptance-checklist.md)，服务端变更和回退见[部署记录](frontend-f6-deployment-runbook.md)。

## 目标与调用链

本批按[已审查的 F6 计划](superpowers/plans/2026-10-10-frontend-f6-acceptance.md)从第 6 步部署进入真实浏览器验收。浏览器通过 Nginx 访问 Vue；`/api/v1` 由 Nginx 转发 Gateway，`/ws-ticket` 和 `/ws` 转发 IM WS。群消息由 WS→Kafka→Push 落库和投递，Task 详情/本人列表和通知列表经 Gateway→Task，通知 WS 只提示客户端重读。AI Ask 与持久 `@AI` 指令经 Gateway/IM→Agent 和模型，确认草稿后再由 Task 建任务、IM 受理机器人回帖。每条链的持久记录与页面状态分别核对，不用 WS ACK 或模型文字替代持久事实。

## 逐步结果

| 步骤 | 实际结果 | 限制 |
| --- | --- | --- |
| 6 正式入口 | **PASS**。原始版本 `ee4fcd8` 上线，036/037 迁移、证书、12 容器和回环入口核对；同源路由 5/5，WS 票据的有效、无效、重放和错误 Origin 均按预期处理 | 公网入口未开放；服务器私有配置未入库 |
| 7 双账号聊天 | **PASS**。最终 `33defb7` 镜像下双向群/私聊 WS 与历史 ID、团队隔离、提及/未读、显式已读、分页和离线补拉全链复测通过；团队 `2108854595408109568` | `475b985` 加租约，`db191c4` 防旧连接晚发布；复审发现全局锁包住 Redis，`33defb7` 改为同用户串行与写入超时 |
| 8 任务/通知 | **PASS**。从持久群消息创建、同键重试、B 本人任务/来源深链、三态、409 页面反馈、通知 WS 提示/持久列表/显式已读通过 | 无提及时来源解码、无 `data` 的状态成功响应分别由 `d3ee2a0`、`5873cf7` 定向修复；仅更新前端容器 |
| 9 AI 草稿 | **PASS（已测成功链，稳定性待观察）**。原运行 `2108838395181551616` 第 1 项编辑、分配 B、确认、同键重试与机器人在线/历史回帖通过；`db191c4` 后新运行 `2108848509196193792` 第 2 项 Vue 跳过 HTTP 200，权威库为 `skipped` 且无任务键/ID。确认消息落库后 Vue Ask HTTP 200，并准确概括讨论 | 两种草稿操作来自不同运行；早期 Ask 503 和另一条 `exhausted` 表明模型有波动；未主动注入 504、回帖失败、少项模型输出 |
| 10 权限/恢复 | **PASS（已测项）**。跨账号 AI 深链拒绝；切群、退出、同一 Chrome 换账号不显示旧私有内容；短时离线恢复后重读；成员离队后历史、任务、通知、重入及离队前 WS 群发送均拒绝。新增真实 Chrome 两次暂停旧群消息 HTTP 200 响应，分别在切到当前群和退出 A、登录 B 后放行，旧消息均未回填 | 旧回包仅覆盖受控的群消息样本；未在维护窗口停止共享 Push |
| 11 桌面/键盘 | **PASS（已测项）**。1280×720、1440×900、1920×1080 的聊天、任务、AI 面板无横向溢出；关闭 AI 焦点返回入口；任务表单 Tab 到团队选择；组合态 Enter 不发送、Shift+Enter 放行换行、普通 Enter 发送；群/任务深链刷新通过 | 键盘使用 Chrome DevTools 事件，未实际操控物理中文输入法；截图只在本地忽略目录 |

## 版本、验证与回退

- 服务器代码 `864468da190403ad68afc73353a7ce0335f23bee`，工作树干净；`deploy` 七份 Compose 覆盖、12 个容器运行，同源路由 5/5。当前 Agent 镜像 `sha256:1f96ae5fe89849b1d52eccc9a780a63b93fc5d84897b5f32c8f2a9194ae10a19`、WS 镜像 `sha256:d8ce184ebfa1aebb56081062f637b9a0ed985136f4df27d5cc48c106bdc62ed2`；本次 Agent 旧镜像 ID 留在服务器 `/tmp/joeyspace-f6-ask-timeout-old-agent-image.txt`，WS 上一镜像留在 `/tmp/joeyspace-f6-user-lock-old-ws-image.txt`。前端镜像仍为 `sha256:58cb7c446a0c85dfb6334bf3e5622033e043301617db8eb9a91521fecf0231fa`。
- MySQL 备份 `/opt/joeyspace-backups/go_im-20261010T060712Z.sql` 的 SHA-256 为 `c9f9039247f726ca887d927825b1862cd69dd83e55c4ec8e404d3515b9e98490`，已在隔离 MySQL 8.0 恢复演练；迁移不做自动 `DROP`。
- `864468d` 后 `go test ./... -count=1` 通过；Ask 模型超时测试先失败后通过，普通模型故障仍保持脱敏 `Unavailable`。此前前端 `npm test` 133/133、`npm run typecheck`、`npm run build` 通过。本轮正式入口匿名路由检查 5/5，新 Agent 镜像下真实 Vue Ask HTTP 200 并准确引用已保存消息；最终 WS 镜像下双浏览器完整聊天回归与 Vue 草稿跳过此前已通过。本机缺少 GCC，`go test -race` 未运行；一次性脚本和桌面截图留在本地忽略目录 `.superpowers/sdd/2026-10-10-frontend-f6-acceptance/`。真实模型超时与完整服务器故障演练未做。
- 本地隔离分支 `codex/frontend-f6-acceptance`；服务器代码与 Agent 镜像对应 `864468d`，本地后续验收文档比服务器新。用户后续已将截至 `2a32296` 的 F6 成果快进到本地与 GitHub `main`，本续报尚未合并或推送；公网入口未开放。

## 本阶段实际修改文件

以 F6 起点 `cdb878a` 为基线，代码/配置：`deploy/.env.example`、`deploy/Dockerfile.frontend`、`deploy/README.md`、`deploy/docker-compose.frontend.yaml`、`deploy/frontend-nginx.conf`、`deploy/verify-frontend-routes.cjs`、`deploy/verify-frontend-routes.test.cjs`、`deploy/verify-vue-browser.cjs`、`frontend/src/messages/sourceContext.ts`、`frontend/src/messages/sourceContext.test.ts`、`frontend/src/tasks/mutations.ts`、`frontend/src/tasks/mutations.test.ts`、`api/multi_draft_skip_flow_test.go`、`rpc/agent/draft_collection_skip_store.go`、`rpc/agent/eino_group_reply.go`、`rpc/agent/eino_group_reply_test.go`、`internal/repository/redis_repo.go`、`internal/ws/client.go`、`internal/ws/hub.go`、`internal/ws/online_lease_test.go`、`internal/ws/task_notification_socket_flow_test.go`、`internal/ws/ticket_test.go`。

共同文档：`docs/architecture-decisions.md`、`docs/frontend-f6-acceptance-design.md`、`docs/frontend-f6-acceptance-checklist.md`、`docs/frontend-f6-deployment-runbook.md`、`docs/frontend-f6-review.md`、`docs/project-plan.md`、`docs/superpowers/plans/2026-10-10-frontend-f6-acceptance.md`、`docs/worktree-collaboration-plan.md`。未跟踪的一次性脚本、截图和 bundle 不计入交付提交。

## 后续待解决

1. 继续观察 Ark 模型的 503 与后台预算耗尽；新增三个独立一次性团队的 Vue Ask 复测均为 HTTP 200，约 14、14、9 秒，新 Agent 镜像再有一次真实 Vue Ask HTTP 200，回答均正确引用已保存消息，证明链路当前可用，不证明持续稳定。Ask 超时与普通模型错误现已分开分类，但早期 HTTP 503 的原始原因无法从当时状态码和日志追溯。云端真实模型超时尚未主动注入，`exhausted` 的页面终态也未单独复测。
2. 在维护窗口单独执行共享 Push 停止/恢复专项；未执行前不可声明该故障恢复能力通过。旧群消息响应在切群、换账号后放行已在云端浏览器各测一次；真实物理中文输入法及模型 504/回帖失败等专项仍未覆盖。
