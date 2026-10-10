# 文档导航

首次阅读 JoeySpace，可以按“项目能力 → 架构 → 运行 → 对应服务代码”的顺序展开。当前实现与运行入口见下面的指南；带日期和阶段编号的文档保留开发时的设计、接口契约和验证证据。

## 先读这些

| 目的 | 文档 |
| --- | --- |
| 了解产品与核心流程 | [项目首页](../README.md) |
| 看懂服务职责、数据归属和调用链 | [架构说明](architecture.md) |
| 自己启动完整项目 | [Docker Compose 部署指南](../deploy/README.md) |
| 开发 Vue 页面 | [前端 README](../frontend/README.md) |
| 调用 HTTP API、准备团队与群 | [Gateway README](../api/README.md) |
| 核对实际通过了什么 | [Vue 验收清单](frontend-f6-acceptance-checklist.md)、[F6 审查报告](frontend-f6-review.md) |

## 按模块读代码

| 模块 | 服务说明 | 深入资料 |
| --- | --- | --- |
| 用户与团队 | [User](../rpc/user/README.md) | [团队接口](team-schema.md)、[退出与恢复](stage7-team-leave-public-rpc-contract.md) |
| 聊天、未读、提及 | [IM](../rpc/im/README.md) | [真实聊天设计](frontend-f3-chat-design.md)、[未读/提及契约](frontend-f3-overview-contract.md) |
| 任务与通知 | [Task](../rpc/task/README.md) | [前端任务 API](frontend-f4-api-contract.md)、[通知 Outbox](stage7-notification-outbox-contract.md) |
| AI 草稿与确认 | [Agent](../rpc/agent/README.md) | [多项草稿设计](agent-multi-draft-design.md)、[后台触发](agent-mention-design.md)、[前端审查方案](frontend-f5-ai-draft-design.md) |
| 桌面交互 | [前端](../frontend/README.md) | [整体设计与调研](frontend-design.md)、[消息契约](frontend-f3-api-contract.md) |

## 运行与验收

- [部署指南](../deploy/README.md)：私有配置、新库/旧库、服务证书、全部 Compose 覆盖、启动和排查。
- [Vue 验收清单](frontend-f6-acceptance-checklist.md)：真实浏览器逐项结果与未覆盖范围。
- [F6 审查](frontend-f6-review.md)：当前核心链路、定向修复及剩余边界。
- [后端阶段 7 验收](stage7-acceptance.md)：数据库、内部链、部署前置和后端验证命令。
- [F6 部署操作记录](frontend-f6-deployment-runbook.md)：维护时的历史版本、迁移、备份和回退证据；其中机器路径、提交和镜像是当次环境快照，不是通用安装参数。

## 设计与过程记录

[项目计划](project-plan.md)记录阶段路线、协作约定和实现历史；[架构决策](architecture-decisions.md)记录方案选择及代价；[worktree 协作](worktree-collaboration-plan.md)记录开发分工。它们用于理解演进过程，首次阅读不必从头遍历。

前端 F1—F6 是开发批次，F01—F19 是具体设计决策编号，不是版本号或独立产品功能。各阶段报告中“当时未部署”“待接线”等表述只说明当次进度，当前结果以最新验收记录为准。

详细文档主要采用以下命名：

| 名称 | 内容 |
| --- | --- |
| `*-design.md` | 业务目标、范围与设计方案 |
| `*-contract.md` | 接口、权限、重试与失败语义 |
| `*-review.md` | 当批修改与验证证据 |
| `superpowers/plans/` | 按步骤的实施计划 |

组件 README 下的折叠“开发过程记录”保留早期细节；普通使用请按组件顶部的当前说明与部署指南操作。
