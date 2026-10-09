# F6 Vue 正式入口与真实环境验收 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 F1—F5 的 Vue 正式页面通过同源入口在真实服务完成双账号聊天、任务、通知和 AI 草稿验收，并留下可复核的结果。

**Architecture:** 一个 Nginx 容器提供 Vite `dist`，同源代理 `/api/v1/` 到 `api-gateway:8082`、`/ws-ticket` 与 `/ws` 到 `im-ws:8081`。保持 Go 服务的数据、权限和写入边界；先本地验证配置，再以服务器回环端口和 SSH 隧道验收，最后才决定公网入口。

**Tech Stack:** TypeScript、Vue 3、Vite、Node 22+、Nginx、Docker Compose；现有 Go/go-zero/gRPC 与 MySQL、Redis、Kafka、WebSocket、Eino 服务。

**Spec:** [F6 统一验收设计](../../frontend-f6-acceptance-design.md)。基线 `cdb878a`，F6 隔离分支 `codex/frontend-f6-acceptance`。

## Global Constraints

- 每步只完成一个可独立审查的目标；任务 1—5 为本地准备批次，任务 6—12 为真实环境批次，各批均不超过九步。每步开始前说明目的、原因、问题和预计文件，完成后记录结果与限制。
- 用户已确认 Nginx 静态托管及同源转发；保持后端业务逻辑、身份、权限、数据归属和消息写入不变。新增框架、中间件或服务边界变更另行讨论并记录。
- 主 agent 统一管理 Compose、依赖、迁移、架构/进度/验收文档和 Git 集成；执行 agent 若使用，只能在独立 worktree/分支修改分配文件，不自行合并 `main`。Task 1 可分给 `D:/zy/GoLang/JoeySpace/.worktrees/frontend-f6-nginx`（只写 `deploy/Dockerfile.frontend`、`deploy/frontend-nginx.conf`）；Task 4 可分给 `D:/zy/GoLang/JoeySpace/.worktrees/frontend-f6-browser`（只写 `deploy/verify-vue-browser.cjs`）。两个 worker 均从主 agent 固定的共同提交开始，派发前重申绝对目录和允许文件。
- 正式页本轮先使用 `http://127.0.0.1:18083`（服务器回环绑定；开发者电脑经 SSH 隧道访问），Nginx 容器监听 80。公网地址、HTTPS/TLS 和证书不在本批预设。保留旧 `/demo/chat` 诊断入口，不把其旧脚本 PASS 算作 Vue PASS。
- `/api/v1/**`、`/ws-ticket`、`/ws` 必须优先于 SPA 回退；HTTP 错误状态/JSON 原样传出，WS Upgrade 与 Origin 不丢失。`index.html` 不做长期缓存，哈希资源可长期缓存。
- 不输出或提交密码、Token、SSH 私钥、私有 `.env`；云端同步、迁移、重建、写入测试数据仅在具体部署差异和回退步骤经用户审查后执行。未运行的项目标 `NOT RUN`，外部条件阻断标 `BLOCKED`，不猜测通过。

## Review Focus

1. API 返回 401/403/404 时是否被 SPA 回退改写成 200 HTML：任务 3 的代理测试与任务 6 的真实请求必须核对状态和内容类型。
2. `/assets/` 缺文件或深链刷新时是否混淆：任务 3 测缺资源 404 和有效深链 HTML；任务 11 浏览器刷新再核对。
3. WS 的页面 Origin、一次性票据和 Upgrade 是否经 Nginx 保真：任务 6 用有效票据连通，并检查无效或重复票据被拒绝。
4. 切账号、离队或 403 后旧私有内容和旧回包是否重现：任务 10 用双账号和真实撤权核对页面与后端事实。
5. 模型超时、预算耗尽或草稿不满五项时是否误报完成：任务 9 逐项记录实际项数、状态及任务/回帖结果。

---

### Task 1: 可构建的 Vue 静态镜像

**Files:** Create `deploy/Dockerfile.frontend`、`deploy/frontend-nginx.conf`。

**Interfaces:** 产出监听容器内 80 的 `joeyspace-frontend:f6` 镜像；使用现有 `frontend/package-lock.json` 执行 `npm ci` 和 `npm run build`；Nginx 的 `/api/v1/`、`/ws-ticket`、`/ws` 上游服务名固定为 `api-gateway:8082`、`im-ws:8081`。Task 2 的 Compose 消费该镜像构建入口。

- [ ] **Step 1:** 写 Nginx 配置：`/assets/` 缺文件返回 404；`/api/v1/` 原路径代理且不启用 SPA 回退；精确 `/ws-ticket`、`/ws` 代理，后者使用 HTTP/1.1、Upgrade、Connection 和足够长的读超时；其他页面 `try_files ... /index.html`。`index.html` 与哈希资源采用上述缓存规则。
- [ ] **Step 2:** 写多阶段 Dockerfile：Node 22 构建、Nginx 运行，运行镜像只复制 `dist` 与配置，不复制源码或 `.env`。
- [ ] **Step 3:** 运行 `docker build -f deploy/Dockerfile.frontend -t joeyspace-frontend:f6 .` 和 `docker run --rm joeyspace-frontend:f6 nginx -t`；预期均退出 0。Docker 不可用则记录 `BLOCKED`，不能称镜像已验证。
- [ ] **Step 4:** 主 agent 复核镜像层与配置后仅保存本任务文件。

### Task 2: Compose 同源入口与 Origin

**Files:** Create `deploy/docker-compose.frontend.yaml`；Modify `deploy/.env.example`、`deploy/README.md`。

**Interfaces:** `frontend-web` 构建 Task 1 的 Dockerfile，宿主端口为 `127.0.0.1:18083:80`；保留基础 Compose 的 `api-gateway`、`im-ws` 服务。扩充 `im-ws.WS_ALLOWED_ORIGINS` 时保留既有 `.env` 自定义值及演示页 Origin，并加入 `http://127.0.0.1:18083`；不触碰私有 `.env`。

- [ ] **Step 1:** 在覆盖 Compose 加 `frontend-web` 和最小 `im-ws` Origin 覆盖；文档给出构建、回环访问与 SSH `-L 18083:127.0.0.1:18083` 示例，并说明后续公网 Origin 必须另行配置。
- [ ] **Step 2:** 对当前私有环境运行 `docker compose --env-file deploy/.env -f deploy/docker-compose.yaml -f deploy/docker-compose.frontend.yaml config --quiet`；预期退出 0 且不打印展开后的秘密。若本机无私有文件，用不包含真实秘密的临时测试环境作静态检查，真实配置留给任务 5/6。
- [ ] **Step 3:** 核对合并结果只增前端服务与必要 Origin，既有演示页 Origin 仍在；保存本任务文件。

### Task 3: 同源路由行为检查器

**Files:** Create `deploy/verify-frontend-routes.cjs`、`deploy/verify-frontend-routes.test.cjs`。

**Interfaces:** 导出 `checkHttpRoutes(baseUrl, fetchImpl)`，接收同源入口和可替换 `fetch`，返回检查结果；CLI 从 `JOEY_F6_URL` 读取地址，默认 `http://127.0.0.1:18083`。只做匿名 HTTP 路由检查，不写真实业务数据；Task 6 复用 CLI。

- [ ] **Step 1:** 先写失败测试：`/messages/teams/2/groups/3` 与 `/tasks/teams/2/3` 得到 HTML；缺失 `/assets/f6-missing.js` 得到 404；未认证 `/api/v1/user/info`、`/ws-ticket` 必须保持非 200 且不是 HTML。错误地返回 `index.html` 的假代理应使检查器失败。
- [ ] **Step 2:** 运行 `node --test deploy/verify-frontend-routes.test.cjs`；预期先因导出函数不存在而失败。
- [ ] **Step 3:** 实现最小检查器与无敏感信息 CLI 输出；运行同一测试预期通过。有可用的真实本地 Compose 时运行 `node deploy/verify-frontend-routes.cjs`；不可用记 `BLOCKED`，任务 6 再运行真实入口。
- [ ] **Step 4:** 主 agent 保存脚本和测试，不把匿名路由检查当 WS 或业务验收。

### Task 4: Vue 专用浏览器验收入口

**Files:** Create `deploy/verify-vue-browser.cjs`、`docs/frontend-f6-acceptance-checklist.md`。

**Interfaces:** `JOEY_F6_URL` 指向 Task 2 的同源站点，`JOEY_CHROME_PATH` 可覆盖 Chrome/Edge；Node 22+ 自带 WebSocket/CDP。脚本与清单只针对 Vue 路由和可见文本；复用旧 `deploy/verify-cloud-browser.cjs` 的一次性账号与 CDP 方式，但不修改旧脚本。详细 AI 项数和失败状态留给人工可重复清单，不自动假定模型输出。

- [ ] **Step 1:** 清单逐项列出双账号聊天、未读/@我、任务/通知、AI 草稿、权限/恢复及 1280×720、1440×900、1920×1080 的预期和证据字段 `PASS/FAIL/BLOCKED/NOT RUN`。
- [ ] **Step 2:** 新脚本先支持真实 Chrome 打开同源 Vue `/login`，完成一次性账号登录、刷新 `/messages`、打开群/任务深链，并核对发出的 API/WS 请求确属同源；失败输出阶段名，不输出密码/Token。先在本地 HTTP/WS 替身上运行脚本，预期这些断言通过；浏览器或替身缺失记 `BLOCKED`。
- [ ] **Step 3:** 主 agent 复核脚本不会误用 `/demo/chat` 或暴露凭证，再保存脚本与清单。

### Task 5: 部署前只读审计和可回退操作单

**Files:** Create `docs/frontend-f6-deployment-runbook.md`；Modify `docs/project-plan.md`、`docs/worktree-collaboration-plan.md`。

**Interfaces:** Task 6 只能按这份操作单执行；其中明确当前 Git SHA、Compose 覆盖列表、036/037 与目标库实际迁移状态、私有证书/Origin、备份与回退命令、旧服务保留方法。脚本与配置在此步完成集中本地审查。

- [ ] **Step 1:** 只读核对目标环境版本、当前容器/端口/证书与迁移状态；不打印凭证。把能验证的事实和仍缺的条件逐项写入操作单，不凭 `init.sql` 推断旧数据卷迁移。
- [ ] **Step 2:** 写出本次精确的同步、迁移、构建、启动、验证和回退命令及作用范围；先完成本地 `npm test`、`npm run build`、`go test ./... -count=1`、Compose `config --quiet`、`git diff --check`，分别记录实际结果。
- [ ] **Step 3:** 主 agent 汇报任务 1—5 的全部文件和本地证据，给用户审查配置与操作单。外部部署/迁移是下一批开始前的最终确认点；未获确认不执行任务 6。

### Task 6: 真实入口部署与路由验证

**Files:** Modify `docs/frontend-f6-acceptance-checklist.md`、`docs/worktree-collaboration-plan.md`；必要缺陷另开小步。

**Interfaces:** 消费任务 5 经用户审查的精确操作单。若当前会话没有可用的云端访问方式，给出用户可运行的命令并将实际执行部分标 `BLOCKED/NOT RUN`；不得猜测部署成功。

- [ ] **Step 1:** 在用户已确认的环境按操作单核对备份、迁移前提和服务状态，再同步/启动正式前端；保留现有 Gateway、WS 和旧演示页可回退路径。
- [ ] **Step 2:** 用任务 3 CLI 核对深链、缺资源、API/票据错误；在真实浏览器用有效一次性票据完成 `/ws` Upgrade，再验证无效或重复票据被拒绝、实际页面 Origin 被允许。
- [ ] **Step 3:** 记录版本、端口、命令退出码和脱敏 HTTP/WS 证据；任何失败先按操作单恢复，再定位，不继续任务 7。

### Task 7: 双账号真实聊天

**Files:** Modify `docs/frontend-f6-acceptance-checklist.md`；若真实缺陷确证，另开定向代码与测试小步。

**Interfaces:** 在任务 6 的 Vue 正式站点使用一次性双账号和团队群；权威依据为 Gateway/IM 历史、未读和 WS 消息 ID，不使用旧演示页结果替代。

- [ ] **Step 1:** 两账号分别登录、加入群并验证会话范围；群聊/私聊互发，核对在线 WS、持久历史、分页和离线补拉的一致性。
- [ ] **Step 2:** 给普通成员结构化提及，核对 `@我` 权威总览；显式标记已读后再次读取，确认阅读本身不会自动清零。
- [ ] **Step 3:** 填入每项实际结果与证据；若中间件/权限阻断记 `BLOCKED`，不冒称前端通过。

### Task 8: 真实任务与通知

**Files:** Modify `docs/frontend-f6-acceptance-checklist.md`；定向缺陷另开小步。

**Interfaces:** 使用任务 7 的真实群消息来源和两账号；Task 本人列表/详情及通知列表为权威数据，WS 通知只提示重读。

- [ ] **Step 1:** 从已保存群消息创建任务，按另一账号实际负责人验证“我的任务”范围、详情、来源定位、三态及版本冲突反馈。
- [ ] **Step 2:** 核对持久通知、实时刷新提示和显式已读；读取权威列表证明数量/状态，不把 WS 提示当最终事实。
- [ ] **Step 3:** 记录每项实际结果、请求 ID 和脱敏页面证据。

### Task 9: 真实 AI 草稿

**Files:** Modify `docs/frontend-f6-acceptance-checklist.md`；定向缺陷另开小步。

**Interfaces:** 使用任务 7 的本人持久 `@AI` 原消息；Agent 状态和集合、Task 创建结果与 IM 机器人回帖分别读取。模型输出项数由真实结果决定，不硬编码五项成功。

- [ ] **Step 1:** 在 Vue 当前群 Ask，再发送明确 `@AI 整理任务`，从原消息进入状态，刷新后按服务端运行 ID 打开真实集合。
- [ ] **Step 2:** 对实际生成的项逐项编辑、补负责人/期限、确认或跳过，核对重复请求不重复建任务；创建成功与回帖受理/在线/历史各自独立记录。
- [ ] **Step 3:** 模型 503/504、预算耗尽、结果不明或少项时保留对应结果和服务端事实，不自动补造成功；记录可复现证据。

### Task 10: 权限和失败恢复

**Files:** Modify `docs/frontend-f6-acceptance-checklist.md`；定向缺陷另开小步。

**Interfaces:** 复用任务 7—9 的临时资源；撤权操作只作用于一次性团队，不碰现有用户团队。Push 停机专项仅在另行确认的维护窗口执行。

- [ ] **Step 1:** 第二账号访问第一账号 AI 深链应被拒绝；切群、换账号、退出登录及 403 后页面不留旧私有消息/草稿，旧在途回包不回填。
- [ ] **Step 2:** 一次性成员离队后复核群历史、任务、通知、原 WS 发送和重新入群拒绝；短时网络断开与恢复核对权威重读。若执行 Push 停机，沿用既有 `verify-cloud-resilience.py --allow-push-stop` 并记录独立结果。
- [ ] **Step 3:** 记录权威状态、浏览器反馈和未覆盖的故障范围。

### Task 11: 桌面与键盘体验

**Files:** Modify `docs/frontend-f6-acceptance-checklist.md`；仅发现明确缺陷才改 Vue/CSS 并补必要检查。

**Interfaces:** 任务 6—10 已可运行的 Vue 正式页面；检查真实浏览器而非静态替身截图。

- [ ] **Step 1:** 在 1280×720、1440×900、1920×1080 核对聊天、任务和 AI 面板的水平溢出、可读性与关闭后焦点恢复；对群聊和任务深链刷新。
- [ ] **Step 2:** 使用实体键盘检查 Tab 到会话/列表/表单、中文输入法组合时 Enter 不误发送、Shift+Enter 换行及面板关闭；记录截图与可重现步骤。
- [ ] **Step 3:** 不把手机全量适配或模型一次成功算作本任务通过。

### Task 12: 汇总审查与阶段状态

**Files:** Create `docs/frontend-f6-review.md`；Modify `docs/project-plan.md`、`docs/worktree-collaboration-plan.md`；缺陷修复文件按实际列入报告。

**Interfaces:** 汇总任务 1—11 的版本、全部实际文件、请求调用链、PASS/FAIL/BLOCKED/NOT RUN、证据路径与回退状态。`main` 合并、推送、部署分别写实际值；任务 6 若未执行，不得把 F6 记成真实环境完成。

- [ ] **Step 1:** 自审全部验收项与用户方案，复跑修复影响范围内的测试、`git diff --check`；有 Go 改动才运行对应 Go 回归和全仓门槛，避免重复无关测试。
- [ ] **Step 2:** 形成可审查报告，列出真实服务证据、未验证项和后续动作；主 agent 做整分支只读审查并保存文档。
- [ ] **Step 3:** 向用户集中汇报第二批七步的逐项结果及全部文件定位，不自行合并 `main` 或公开部署入口。

## 执行交接

已确认的执行方式为主 agent 调度、最多三个执行子 agent 且各自独立 worktree；主 agent 统一契约、配置、共同文档和 Git 集成。任务 1 与任务 4 可并行读取既定接口，但必须分别在允许文件内工作；任务 2 依赖任务 1，任务 3 依赖任务 2，任务 5 汇总本地成果；任务 6 依赖用户审查具体部署差异，任务 7—11 按真实链路顺序执行，任务 12 汇总。每一步先报告目标/原因/文件，完成后报告实际验证与限制；若环境不足，保持诚实的 `BLOCKED/NOT RUN` 而不跳过前置验收。
