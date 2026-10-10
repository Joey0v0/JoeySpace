# JoeySpace Web 前端

使用 TypeScript、Vue 3、Vue Router 和 Vite 的桌面协作工作区。默认先看消息与团队讨论，再处理自己的任务；AI 草稿审查通过当前群的侧栏进行。页面已接入真实后端，不依赖固定样例数据。

[项目首页](../README.md) · [整体架构](../docs/architecture.md) · [交互设计](../docs/frontend-design.md) · [部署指南](../deploy/README.md)

## 当前页面

| 路由 | 功能 |
| --- | --- |
| `/login` | 注册、登录与本人身份验证 |
| `/messages` | 会话导航、跨会话未读、`@我` 筛选 |
| `/messages/direct/:peerId` | 已有私聊的历史、发送、分页与显式已读 |
| `/messages/teams/:teamId/groups/:groupId` | 团队群讨论、本人入群、提及、来源定位、Ask 与本人 `@AI` 草稿审查 |
| `/tasks` | 本人开放/已完成任务、团队筛选、跨团队通知与逐条已读 |
| `/tasks/new` | 人工创建任务，选择成员、期限及可选消息来源 |
| `/tasks/teams/:teamId/:taskId` | 任务详情、状态更新与来源讨论 |

团队创建、添加成员、角色和退出目前由[HTTP API](../api/README.md#准备团队与群聊)提供。新账号不会自动加入团队；私聊目录由既有消息形成，页面没有独立的好友管理或发起新私聊界面。图片/文件发送、复杂看板、完整手机适配未纳入当前工作区。

## 本地开发

推荐 Node.js 24，使用仓库提交的 `package-lock.json`：

```sh
cd frontend
npm ci
npm run dev -- --port 5173 --strictPort
```

浏览器打开 **http://127.0.0.1:5173**。Vite 按 [vite.config.ts](vite.config.ts) 代理请求：

| 浏览器路径 | 本地后端目标 |
| --- | --- |
| `/api/**` | `http://127.0.0.1:8082`，go-zero Gateway |
| `/ws-ticket` | `http://127.0.0.1:8081`，WS 票据 |
| `/ws` | `ws://127.0.0.1:8081`，WebSocket Upgrade |

后端必须运行。WS 的 `WS_ALLOWED_ORIGINS` 需要包含实际页面 Origin，例如 `http://127.0.0.1:5173`；`localhost` 和 `127.0.0.1` 是不同 Origin。使用 Compose 时在私有 `deploy/.env` 设置，并按原有的完整覆盖组合更新 `im-ws`。只启动 Vite 不会启动后端；显示登录页不代表 API 或 WS 可用。

也可保持云端后端，通过本机 SSH 转发 8082 和 8081 接入开发代理；保持相应 Origin 配置一致。正式部署的浏览器无需单独连接两个后端端口，统一经 Nginx 同源代理。

## 检查与构建

在 `frontend` 目录执行：

```sh
npm test
npm run typecheck
npm run build
```

测试使用 Node 原生测试运行器覆盖身份、API 解码、会话、请求竞争、任务/通知和 AI 审查逻辑。`build` 自带类型检查，静态文件输出到 `dist/`；Compose 的 [Dockerfile.frontend](../deploy/Dockerfile.frontend) 使用 Node 22 构建并交给 Nginx 托管。

`npm run preview` 仅供检查构建产物，不自动沿用开发期 API/WS 代理。需要真实业务时使用[正式 Nginx 入口](../deploy/README.md#页面访问)。`scripts/check-preview.cjs` 保留为早期布局检查辅助脚本；正式浏览器验收使用 `deploy/verify-vue-browser.cjs` 和[验收清单](../docs/frontend-f6-acceptance-checklist.md)。

## 代码组织

```text
src/
├── auth/       # 标签页会话、本人核验、注册登录
├── api/        # 同源 HTTP、错误映射、数据解码
├── messages/   # 团队/私聊目录、历史、离线、未读、来源
├── realtime/   # 一次性票据、WS 重连/发送状态、任务提示
├── tasks/      # 本人列表、创建/详情/状态、通知
├── agent/      # Ask、原指令状态、草稿读取/编辑/逐项决策
├── router.ts   # 深链与身份路由守卫
└── App.vue     # 模块导航与工作区容器
```

局部状态使用 Vue 自身和业务控制器，没有引入全局状态库或完整 UI 框架。Token 存在当前标签页的 `sessionStorage`；刷新后通过本人 API 复核。切群、退出或换账号后，旧请求结果会被丢弃。

已读必须显式确认；WS ACK、离线 ACK 和读取历史各有不同含义。创建或确认超时后先重读服务端状态，同一创建重试保留请求键；草稿编辑与确认检查版本；普通模型错误、冲突和结果不明分别反馈。

截至 2026-10-10，正式云端 Vue 核心流程已由真实双账号 Chrome 验证，包括消息、任务/通知、AI 成功链与权限恢复；桌面检查覆盖 1280×720、1440×900、1920×1080。模型稳定性与物理中文输入法等限制见[F6 报告](../docs/frontend-f6-review.md)。
