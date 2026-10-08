# JoeySpace 前端

电脑浏览器优先的消息界面，使用 TypeScript、Vue 3、Vite。Node.js 24 已在本机验证。

## 本地启动

在 `frontend` 目录执行：

```sh
npm ci
npm run dev
```

打开终端显示的本地地址。`/messages` 是未读总览；点会话可查看讨论，`/tasks` 是任务模块的后续接入说明。

## 构建与检查

```sh
npm test
npm run typecheck
npm run build
npm run preview
```

`build` 会先做类型检查，静态文件输出到 `dist/`。`preview` 需要先运行 `build`。浏览器检查脚本为 `node scripts/check-preview.cjs`，要求本地 Chrome 已用独立 profile 和远程调试端口打开预览页，并设置 `FRONTEND_PREVIEW_URL`（如 `http://127.0.0.1:4173/messages`）和 `FRONTEND_BROWSER_PROFILE`（该 profile 的绝对路径）。检查会在 profile 内写入三个桌面尺寸及一个会话截图。

## 当前数据范围

会话、未读数、@我和消息正文均来自固定的本地样例。页面顶部会持续标注“界面样例”。查看会话不会改变未读数；输入与发送已禁用。当前没有登录、真实消息接口、任务列表或服务端 @提及。后续接入这些能力时以 [前端设计](../docs/frontend-design.md) 和现有后端权限、已读契约为准。

首批仅有 Vue 和 Vue Router 两个直接运行依赖，`src/` 下 12 个源文件（含模型测试）。本机生产构建输出约 14.2 kB CSS 和 101.6 kB JavaScript，便于后续核对依赖增长。
