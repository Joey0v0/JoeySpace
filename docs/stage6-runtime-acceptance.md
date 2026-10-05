# 阶段 6：群内 @AI 真实服务验收准备

日期：2026-10-05。本页描述**最终统一部署时才执行**的验收顺序；当前只检查本地代码、配置和自动化测试。用户决定完成整体代码后再同步云端。本页各项真实 MySQL/Kafka/模型/浏览器结果均为**未验收**，不能用本地替身测试替代。[原有单项与多项草稿清单](stage6-acceptance.md)可作补充；本页以新增的群内后台触发链为主。

## 1. 启动前核对（未执行）

1. 在隔离验收环境记录代码提交、Compose 项目名、数据卷及备份，核对已有库的迁移版本。旧数据卷不会重跑 `mysql/init.sql`；022 IM Outbox、023 Agent Inbox、024 租约/预算、025 结果关联、026 持久退避，以及先前草稿/机器人/逐项任务迁移，必须按实际库结构依次补齐，不能盲目重跑。具体文件见[迁移目录](../deploy/mysql/migrations)和[部署说明](../deploy/README.md)。本轮没有执行迁移。
2. 准备真实方舟接入点、API Key 和可接受预算，保存在不入库的 `deploy/.env`。当前 Agent 生产进程在监听前构造模型；仓库测试替身不是可启动的模型模式。接入点和预算尚未确定，因此现在不能完成真实端到端演示。
3. 准备独立的机器人和触发服务证书，核对每个证书的用途、信任 CA 和精确 DNS SAN。触发链四个私有目录见[环境模板](../deploy/.env.example)：User 服务端 `user.go-im.internal`、IM 服务端 `im.go-im.internal`、IM→User 客户端 `im.go-im.internal`、Agent→IM 客户端 `agent.go-im.internal`。机器人回帖另用 bot 覆盖中的证书目录；不共享私钥。测试临时证书不可部署，证书更新需重启相关进程。
4. 仅在最终启用时，把**现有私有** `deploy/docker-config.local.yaml` 的 `kafka.agent_trigger_enabled` 改为 `true`，核对 `topic_agent_trigger: agent_task_triggers` 与[触发覆盖文件](../deploy/docker-compose.trigger.yaml)完全一致。Push 只读取 YAML；给 `.env` 增加同名开关不会生效。保留私有文件的其他凭证，不能重新复制脱敏模板覆盖。
5. 从 `deploy` 目录检查完整 Compose 合并；此命令只解析配置，不启动服务：

   ```sh
   docker compose --env-file .env --profile agent -f docker-compose.yaml -f docker-compose.bot.yaml -f docker-compose.trigger.yaml config --quiet
   ```

   触发覆盖只启用 User:9007、IM:9006 的容器内部 mTLS 端口和 Agent 通知消费/worker；机器人覆盖启用 IM:9005。三个专用端口均不映射宿主机。四组触发证书按只读 bind 挂载且不自动创建宿主路径；Agent 消费组和聊天 Push 消费组分开。基础 Compose 继续默认关闭群内后台链路。当前电脑没有 Docker 命令，本轮尚未执行 `compose config`、镜像构建或启动。
6. 按现有项目/卷实际状态依次升级 User、IM、Push、Agent、Gateway；Agent 依赖真实模型配置和 Kafka，Push 启用 Outbox 发布。不要使用 `down -v`。检查各服务日志、专用 TLS 握手与 Kafka 独立触发 Topic；普通 Gateway HTTP 可用**不能**证明 Agent Consumer/Worker 正常，当前故障只在日志中报告，没有单独健康接口。

## 2. 正常业务闭环（未执行）

用隔离团队、测试群、发起人 A 和同群成员 B；先分别核对 A 的登录、团队/群资格、人工任务创建和群历史。打开 Gateway `/demo/chat`，先取得本人身份，再进入团队群并连接 WS。

| 顺序 | 操作 | 必须观察的事实 |
| --- | --- | --- |
| 1 | A 发群文本 `@AI 整理任务 请整理发布检查任务`；单独 `@AI 整理任务` 不触发。 | WS ACK 只表示聊天入队。待 Push 保存后，群历史有持久消息 ID `M`；`im_agent_trigger_outbox` 对 `M` 恰有一行。原发送人不靠 WS 回推取得 `M`，须刷新最新群历史。 |
| 2 | 查看 Outbox、Agent Inbox，再从 A 的原消息按钮查询 `GET /api/v1/teams/{team}/groups/{group}/agent-triggers/{M}`。 | `published=1` 只表示 Kafka 通知受理；Inbox `queued/running` 不是已生成。`completed` 返回同范围的正 run ID `R`；通知尚未入库时可以 404，worker 关闭时状态入口不可用。B、离群 A 不得看到结果。 |
| 3 | A 打开 `GET /api/v1/agent/runs/{R}/drafts`，逐项编辑、跳过或确认。 | 每项保留来源和原时间依据；重名负责人和 `needs_input` 截止时间须本人解决。确认使用页面刚读取的版本/完整审查快照；未确认前 Task 表不新增相应任务。 |
| 4 | A 逐项确认已审查草稿，必要时先重读再按原键显式重试。 | 每项创建成功才有正 Task ID；重复确认仍是同一 Task。`creating` 表示结果不确定，不能声称成功；跳过项 Task ID 为零。 |
| 5 | 独立查看回帖状态；失败时先重读目标项，再显式调用 `/api/v1/agent/runs/{R}/drafts/{i}/reply/retry`。 | `accepted` 只是 IM 持久受理/Kafka ACK，不是成员已收到。B 的实时、离线或群历史应看到同一 `bot-task:{R}[:i]` 卡片；历史卡片应以实际 HTTP 消息字段形状检查。 |

建议保留以下**只读** SQL 快照，使用实际值替换 `M/R/A`，不保存密码、Token、私有消息正文或原始租约 token：

```sql
SELECT message_id, published FROM im_agent_trigger_outbox WHERE message_id = M;
SELECT message_id, status, received_at, model_attempts, model_started,
       lease_until, SHA2(lease_token, 256) AS owner_fingerprint,
       retry_after, retry_failures, result_run_id
FROM agent_task_trigger_inbox WHERE message_id = M;
SELECT id, team_id, group_id, initiator_id, request_key, draft_mode, item_count
FROM agent_runs WHERE id = R AND initiator_id = A;
SELECT item_index, status, revision, task_request_key, task_id
FROM agent_task_drafts WHERE run_id = R ORDER BY item_index;
```

## 3. 恢复与撤权（隔离环境，未执行）

- **重复通知**：在专用测试 Topic/Group 以原 `M`、固定 Kafka Key `agent-trigger:tasks:M` 重发 `{"version":1,"action":"extract_tasks","message_id":"M"}`；不重发聊天消息、不清空记录或重置现有消费组。Inbox 仍仅一行，`received_at` 不变；已完成的 `result_run_id`、草稿项数不变，耗尽的预算不重置。running 的正常续租/完成可改变运行字段，不能误判为重放写入。
- **真实崩溃恢复**：只在隔离服务上，记录 Inbox 的数据库时间、租约截止与 token 指纹；在 running 且已记录模型预算后终止 Agent 进程，再启动同配置。数据库时钟未超过旧截止前不得再次领取；过期后新 token 才可接手，既有预算保留，累计模型许可至多两次，完成仅一份 run。普通平滑重启不能冒充崩溃；单进程测试也不能证明旧存活 worker 的写入隔离，后者需另设并行实例和不同 Snowflake 节点。
- **当前权限撤销**：现无公开离队/离群接口。只能对隔离测试成员资格保存快照、限定范围撤销并恢复，不更改真实用户数据，也不能因此声称离队流程已验收。首次来源读取前撤权应零模型调用/零草稿；模型预算已记后撤权应保留次数、拒绝保存并持久退避。模型窗口短，未确实命中就标“未验证”。已完成后撤权，状态、集合读取和本人后续写入也应拒绝；单纯 Token 过期只验证本人查询，不能作为无 Token 后台 worker 必须停止的条件。
- **重复确认/回帖**：用原审查快照重试同一草稿项，Task 的 `(creator_id, request_key)` 只对应一个 Task ID；回帖失败重读后显式重试，应保持同一 `msg_id`。Kafka 结果不明时允许相同消息身份再受理，不能要求每个实时帧恰好一次；按 `msg_id` 去重并核对历史存储。若要证明“Task 已提交但响应丢失”，必须实际观测 Task 行存在且草稿仍 `creating`，不能仅停 Task 服务模拟。

所有结果记录代码版本、迁移版本、测试账号范围、脱敏请求/响应、数据库行数、日志和恢复动作。Outbox、Inbox、租约、预算和消费组 offset 是恢复依据，不手工清空或重置。当前没有 worker 对外健康/暂停入口、耗尽项重跑入口或确定性丢响应故障注入；这些场景不能靠现有替身测试宣称真实验收。下一轮应以本页核对仍缺的可运行操作，最终统一部署后逐项填写真实结果。
