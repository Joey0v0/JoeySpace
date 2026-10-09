# F5 任务 3 问答与指令状态契约

日期：2026-10-10。主工作区 `D:/zy/GoLang/JoeySpace/.worktrees/frontend-f5-ai-draft`；执行工作区 `D:/zy/GoLang/JoeySpace/.worktrees/frontend-f5-review-state`，分支 `codex/frontend-f5-review-state`。执行者从主 agent 发布本契约后的共同提交进入。设计依据为[F5 方案](frontend-f5-ai-draft-design.md)与[八任务计划任务 3](superpowers/plans/2026-10-10-frontend-f5-ai-draft.md#任务-3临时-ask-和本人指令状态控制)。

## 目标及允许文件

本步只实现当前群临时问答与本人原指令状态读取/轮询的 TypeScript 状态控制。用户在群里看到真实等待、处理中、失败或已生成状态；关闭面板、换账号/群/指令后的旧回包不得覆盖当前状态。集合读取和编辑属于任务 4—6，本步不提前实现。

执行 agent 仅新增或编辑：

- `frontend/src/agent/review.ts`
- `frontend/src/agent/review.test.ts`

主 agent 独占共同文档、Git 保存、集成验证和后续任务分配；执行 agent 不改 Vue、API/模型、package 脚本、后端、协议、迁移、依赖或其他测试，不自行提交、派生子 agent、合 main、推送或部署。

## 共享接口与状态

- 导出 `initialAgentReviewState(): AgentReviewState`，由 Vue 用 `reactive(initialAgentReviewState())` 持有。状态至少区分 `closed/ask/trigger` 模式、当前团队群/原消息范围、当前问题与回答、问答/状态请求忙闲、权威 Trigger 状态及安全错误提示。字段名与具体内部组织可在本步固定，后续任务 4—6在此上扩展，不额外创建第二套状态所有者。
- 导出 `createAgentReview(agentApi, identity, state)`，其中 `agentApi` 类型是任务 2 的 `ReturnType<typeof createAgentApi>`，`identity` 是现有 `createSession` 返回类型。提供 `openAsk({teamId,groupId})`、`ask(question)`、`openTrigger({teamId,groupId,messageId})`、`refreshTrigger()`、`close()`、`dispose()`；`openTrigger` 首次读取 Trigger，`refreshTrigger` 对同一当前范围重新读取。
- Ask 只在当前面板内显示 `{answer}`，不成为聊天消息、任务或跨会话历史。问题修剪后为 1—2000 Unicode 码点，底层 API 负责进一步校验。503/504、网络失败保留当前问题并给“结果未确认，可重新提问”的可读提示；不虚构答案。
- Trigger 只接受任务 1/2 已核对的权威四状态。`queued/running` 在面板打开时每 5 秒轮询；打开后满 2 分钟改为每 15 秒，满 10 分钟停止自动轮询并保留手动刷新。`exhausted` 和 `completed` 停止轮询，后者保存返回的 `run_id` 供任务 4 使用；不展示假进度、旧指令重试或从消息正文推断运行 ID。
- 单一计时器、单一状态请求在途。一个请求未完成时不启动下一次；关闭、切换范围、账号代际变化、403/404 或 dispose 时停止轮询、清空私有状态并忽略旧成功/错误回包。普通 503/504 可保留当前安全状态和手动刷新，不把后台执行标成失败。新范围可立即发起请求，不等待旧范围的请求结束。
- 状态控制不自行写浏览器地址，也不调用 DOM；任务 7 由页面处理路由和焦点。定时器与时间源可通过可选注入或 Node 内置假时钟测试，不新增依赖。

按 TDD 写失败测试后实现，运行 `node --test src/agent/review.test.ts`、`npm run typecheck` 和现有 `npm test`；记录 RED/GREEN 命令和关键输出。主 agent 复核并安排只读规格/质量审查。本步不改 `package.json`，新测试暂由定向命令运行。
