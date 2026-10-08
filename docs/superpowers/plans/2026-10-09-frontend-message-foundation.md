# JoeySpace 消息页面首批开发 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 交付一个视觉与操作可审查的电脑端消息页面：未读总览、团队群与私聊合并、会话切换、固定任务入口和清楚的样例状态。

**Architecture:** 在独立 frontend 目录建立 Vue 单页应用，以 Vue Router 管理会话选择和浏览器返回。布局与页面组件按用途组织，样例与展示模型分开；本批不依赖后端新接口。原生 CSS 和 Vue 状态足够完成当前页面，后续按真实业务需要增加依赖。

**Tech Stack:** TypeScript、Vue 3、Vite、Vue Router；vue-tsc 做类型检查；Node 内置测试与现有 Chrome DevTools 验证方式。

**Spec:** [前端设计 v0.2](../../frontend-design.md)，重点实施第 1、3、4、6、10、11 节的 F1 页面范围。

## Global Constraints

- 主要在电脑浏览器使用，验证 1280×720、1440×900、1920×1080。
- 聊天优先，登录后的目标入口是未读总览；本批使用显式标注的样例，不假装登录或后端已接入。
- 团队群和私聊按会话合并；打开会话后模块导航和会话列表保留。
- 核心体验优先，控制非核心范围。看板拖拽、统计图表、复杂设置、多主题和完整手机体验后置。
- 样例阅读不修改未读数；真实已读、发送、任务写入和 AI 确认在后续批次接线。
- 运行时首批只需要 Vue 与 Vue Router；使用原生控件/CSS，不安装完整管理后台模板。
- 页面和消息 ID 保留字符串，避免 JavaScript 大整数精度丢失。
- 每批最多九个独立小步骤；每步开始说明目的与文件，按用户要求集中审查结果。
- 基础配置用官方常规结构，不为配置写镜像测试；业务选择行为按 TDD 验证。

## Review Focus

1. 大于 JavaScript 安全整数范围的会话 ID：选择和返回必须保持原字符串，归 Task 2 的模型测试和浏览器检查。
2. 未知会话地址：显示可理解的不可用状态并保留导航，不能偷偷打开其他会话，归 Task 2 的浏览器检查。
3. 无匹配结果：搜索或 @我筛选为空时说明当前筛选没有结果，归 Task 2 的模型测试和浏览器检查。
4. 看讨论与返回总览：仅导航不清零未读数，归 Task 2 的无变异测试及 Task 3 的完整页面检查。
5. 桌面窗口和键盘：1280×720 下输入区与导航不被裁切，列表、筛选和返回都能通过键盘操作，归 Task 3。

## 文件职责与执行方式

主 agent 统一维护依赖、锁文件、共享文档和提交基线。沿用已确认的独立 worktree 协作方式；本批任务共享同一界面，不机械拆成三人并行。主 agent 可安排一个执行 agent 顺序实现页面并集中审查，或在已隔离的主执行工作区完成依赖与组合验证。

现有三个 worktree 指向旧路径且被 Git 标为 prunable，不能直接拿来作为执行目录。执行前验证 .worktrees/ 已忽略，从共同提交建立真实可用的绝对目录与 codex/ 分支；未经检查不清理旧记录。当前方案与计划文档的未提交修改应保留并纳入共同基线，不能让执行 worktree 缺失批准方案。

新增文件只在对应任务需要时创建，不生成所有未来模块的空目录。

## Task 1: 可启动的页面框架

**Files:**
- Create: frontend/package.json、frontend/package-lock.json、frontend/index.html。
- Create: frontend/tsconfig.json、frontend/vite.config.ts。
- Create: frontend/src/main.ts、frontend/src/router.ts、frontend/src/App.vue、frontend/src/style.css。
- Create: frontend/README.md。
- Modify: .gitignore，增加 frontend/node_modules/、frontend/dist/。

**Interfaces:**
- Produces: App.vue 的固定模块导航和 RouterView；入口“消息”“我的任务”有中文标签。router.ts 注册 /messages 与 /tasks，Task 1 用简单 render 函数显示准备说明，Task 2 替换为实际页面。
- Produces: 全局 CSS 变量，沿设计稿的背景、正文、主色、间距及焦点样式。
- Consumes: 本机已核对 Node v24.15.0 与 npm 11.12.1；安装时检查所选稳定版本的 engines 并锁定，不用预发布版。

- [ ] 核对隔离目录、分支及设计文档，确认执行 agent 允许文件；主 agent 准备共同基线。
- [ ] 建立最小 package 配置：dev、typecheck、build、test、preview 脚本。test 使用 node --test src/messages/model.test.ts；配置脚本使用 TypeScript 可擦除语法。
- [ ] 主 agent 安装 Vue、Vue Router 与必要开发依赖 Vite、@vitejs/plugin-vue、TypeScript、vue-tsc、@types/node，生成 npm 锁文件；tsconfig 启用 strict、noEmit、allowImportingTsExtensions，并支持 Node 测试类型。
- [ ] 实现固定导航与基础排版；“消息”进入 /messages，“我的任务”进入 /tasks；路由内容在 Task 2 补齐。
- [ ] 配置核对：npm run typecheck 与 npm run build；成功输出类型检查退出 0、Vite 构建退出 0。本任务只核对标准工程配置及可启动框架，不宣称页面交互完成。
- [ ] 主 agent 审查并保存本任务精确文件，避免 git add . 混入其他工作。

## Task 2: 未读总览与会话切换

**Files:**
- Modify: frontend/src/router.ts，将入口说明替换为实际页面及会话地址。
- Create: frontend/src/messages/model.ts、frontend/src/messages/model.test.ts、frontend/src/messages/sample.ts。
- Create: frontend/src/messages/MessagesPage.vue。
- Create: frontend/src/messages/ConversationList.vue、frontend/src/messages/UnreadOverview.vue、frontend/src/messages/ConversationView.vue。
- Create: frontend/src/TaskPreview.vue。
- Modify: frontend/src/main.ts、frontend/src/style.css。

**Interfaces:**
- ConversationSummary: readonly key:string、kind:'group'|'direct'、title:string、teamName?:string、unreadCount:string、mentioned:boolean、preview:string、updatedAt:number。
- MessagePreview: readonly id:string、senderName:string、content:string、own:boolean、timeLabel:string。
- getUnreadConversations(items: readonly ConversationSummary[], filter:'all'|'mentions'): ConversationSummary[]，只筛出未读；有 @ 的优先，然后按 updatedAt 倒序，不改变输入。
- findConversation(items: readonly ConversationSummary[], key:string): ConversationSummary | undefined，精确比较字符串。
- SampleData: conversations 和按会话 key 存放的消息，不包含真实账号/凭证。样例 @ 状态只用于预览，不冒充服务端识别。
- Routes: /messages 显示总览；/messages/teams/:teamId/groups/:groupId 和 /messages/direct/:peerId 显示相应样例会话；/tasks 仅呈现任务模块后续接入的说明。
- 固定样例：至少一个有 @ 的团队群、一个普通未读群、一个未读私聊及一个已读私聊；私聊 ID 包含 9007199254740993。

- [ ] 先写模型行为测试，使用 Node 内置 assert/test，不新增测试框架。断言包括：
  - all 排除 unreadCount='0'，同一会话只出现一次。
  - mentions 只返回有 @ 且未读的样例。
  - 相同数据重复读取后原顺序、计数与对象内容不变。
  - findConversation 精确查到 direct:9007199254740993；错误 key 返回 undefined。
  - 空数组以及没有 @ 的数组得到空筛选结果。
- [ ] 运行 npm test，确认目标行为未实现而失败；仅为测试可执行提供必要类型/导出，不预写筛选逻辑。
- [ ] 实现上述两个函数、只读样例和路由；运行 npm test，确认全部断言通过。
- [ ] 实现 MessagesPage 组合会话列表与右侧总览/讨论；使用真实按钮或链接、可读名称和选中状态。点击会话与浏览器返回均通过路由处理。
- [ ] 总览显示群/私聊标签、摘要、时间、未读数；提供全部未读 / @我筛选。会话列表搜索限定样例目录，空结果解释筛选条件。
- [ ] 会话视图显示头像占位、发送者、日期/时间、中文正文及机器人标识。保留输入区布局；本批发送控件禁用并以统一样例提示说明状态，不回显“发送成功”。
- [ ] 未知会话显示“当前会话不可用”，提供返回总览，左侧导航和列表保持；未读数在查看会话后不清零。
- [ ] 运行 npm test、npm run typecheck、npm run build。成功标准为全部模型测试通过、两个工程检查退出 0。
- [ ] 主 agent 审查页面及允许文件，再保存本任务成果。

## Task 3: 页面质量核验与审查交付

**Files:**
- Create: frontend/scripts/check-preview.cjs，仅作浏览器检查，不进入应用构建。
- Modify: frontend/src/style.css 和上述页面中经实际检查发现的问题。
- Modify: frontend/README.md、docs/frontend-design.md、docs/project-plan.md，记录启动和实际验证范围。

**Interfaces:**
- check-preview.cjs 使用 Node 原生 fetch/WebSocket 连接独立 Chrome DevTools 会话；方式参考现有 deploy/verify-direct-unread-browser.cjs，不引入浏览器测试框架。
- 参数使用 FRONTEND_PREVIEW_URL 与 FRONTEND_BROWSER_PROFILE；只访问本地静态预览，不需要 Token 或真实后端。
- 检查失败退出非零；成功输出检查项目，截图放在临时输出目录，不写入应用源码。

- [ ] 先写浏览器断言：默认总览、选择私聊后列表保留、点击总览恢复、未知会话不回落、大 ID 不变、搜索无结果、查看会话不修改未读。
- [ ] 从已有 F1 可运行框架执行浏览器断言；未满足行为时必须先观察失败，再修正相应页面；全部已有功能通过时保留为组合回归，不为制造失败回退已正确的代码。
- [ ] 检查 1280×720、1440×900、1920×1080 的区域尺寸、水平溢出、聊天输入位置与列表独立滚动；保存截图并逐张查看。长消息不撑破页面。
- [ ] 用键盘实际检查会话链接、搜索、筛选、返回及任务入口；焦点可见。记录手动核验结果，不能以 CSS 存在代替操作通过。
- [ ] 运行 npm test、npm run typecheck、npm run build 和浏览器检查；仅在失败或实际发现问题时扩大测试。
- [ ] README 给出 npm ci、npm run dev、npm run build、npm run preview，以及样例数据范围。记录 runtime 依赖、源文件数与构建输出，方便判断后续是否增加了不必要复杂度。
- [ ] 主 agent 汇报全部实际修改文件、已验证与未验证范围，以及进入真实接口接线的下一小步；用户在主聊天审查。

## 本批验收与后续范围

交付结果是可启动、可浏览并可操作会话选择的正式页面骨架；样例数据与真实业务接线状态明确。模型行为、桌面布局和浏览器导航通过实际检查后，才能把 F1 标为完成。

F2 起再逐步接登录、已有聊天接口与确有必要的目录查询。Task/Agent 服务、数据库迁移、普通成员提及协议及生产部署不包含在本计划中，相关契约另行审查。整个前端采用已有后端业务语义，核心阅读与错误体验持续保持。

