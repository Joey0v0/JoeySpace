# 阶段 3：团队群聊验收清单

这份清单用于项目整体完成后统一同步、联调阶段 3。当前代码通过本地替身测试，不等于真实 MySQL、Redis、Kafka、WebSocket 和容器链路已经通过；未实际执行的项目保持未勾选。目标是验证两个账号能在同一团队群收发、查询消息，越权请求被拒绝，断线后能取回并确认消息。

## 已在本机验证（2026-09-29）

- [x] 项目根目录运行 `go test ./...`：全部通过，覆盖 Gateway、用户与团队 RPC、IM RPC、WS、Push 和 Repository 的本地测试或替身测试。
- [x] 项目根目录运行 `node --test examples/chat.test.cjs`：15 项通过，覆盖演示页的创建、选群、入群、历史游标、大整数 ID、离线确认与去重逻辑。
- [ ] 本机 Docker Compose、真实数据库及双浏览器联调：本机没有可用的 `docker` 命令，尚未执行。

## 部署前提与迁移（待执行）

1. 在隔离的联调环境核对代码版本、Compose 项目名、现有 MySQL 数据卷和私有配置；保留已有凭证和数据。凭证配置方法见 [部署基线](../deploy/README.md)。在 `deploy` 目录运行 `docker compose --env-file .env -f docker-compose.yaml config --quiet`，只检查配置，不输出完整配置。核对 `.env` 中的 JWT 密钥与旧 API 私有 YAML 一致，三个 Snowflake 节点号互不相同。
2. 备份已有 `go_im` 数据库，检查实际表和索引。新数据库由 [init.sql](../deploy/mysql/init.sql) 初始化；已有数据库按实际未执行的部分核对 [001 团队表](../deploy/mysql/migrations/001_teams.sql)、[002 离线唯一约束](../deploy/mysql/migrations/002_offline_message_unique.sql)、[003 群归属](../deploy/mysql/migrations/003_group_team_id.sql)、[004 创建幂等键](../deploy/mysql/migrations/004_group_create_request_key.sql)、[005 群历史索引](../deploy/mysql/migrations/005_team_group_history_index.sql) 的顺序。迁移脚本不是可反复执行的初始化脚本；先确认现有结构，避免重复 `ALTER TABLE`。执行 002 前先检查脚本中的重复离线记录查询；有重复时先决定保留哪条，不自动删除。
3. 在 `deploy` 目录运行 `docker compose --env-file .env -f docker-compose.yaml ps --all`，确认 MySQL、Redis、Kafka 和旧 API 状态；按[部署基线](../deploy/README.md)核对私有配置与现有数据卷，更新 `im-api`、`im-ws`、`im-push`、`api-gateway`、`user-rpc`、`im-rpc` 六个 Go 服务后再做业务验证。部署基线里的云端命令只覆盖早期两个新服务，不能作为本阶段全部服务的更新命令。Compose 对 RPC 只使用 `service_started`，仍需实际请求确认服务已就绪。不要把私有 YAML、Token 或数据库密码复制到验收记录。

## 双账号业务链（待执行）

准备团队拥有者 A、同团队普通成员 B、非团队成员 C；先用已有团队接口创建团队并加入 B，再从 Gateway 的 `GET /demo/chat` 打开演示页，在独立浏览器会话登录 A、B。每项记录 HTTP 状态与业务 `code`、群 ID、消息 `msg_id`，必要日志先脱敏。

| 验收动作 | 应看到的结果 | 状态 |
| --- | --- | --- |
| A 创建团队群，再用相同 `Idempotency-Key` 和内容重试；B 尝试创建 | A 两次得到同一字符串群 ID；B 被拒绝；相同键换群名返回冲突 | 未验收 |
| B 加载群目录，在入群前尝试发送或读历史；C 查询目录、尝试入群或读历史 | B 可见目录但未入群时不能发送或读历史；C 被拒绝。目录可见本身不等于已入群 | 未验收 |
| B 选择群、本人入群，再重复入群 | B 两次入群均成功，之后可发送和读取历史 | 未验收 |
| A、B 各连 WS，A 向该群发送；B 断开后 A 再发送消息 M1 | A 收到回显所发 `msg_id` 的 ACK；在线 B 收到群消息；断开的 B 留有 M1 离线记录。ACK 只代表入 Kafka，不代表 B 已收到 | 未验收 |
| B 保持断线，使用 HTTP 两次拉取未确认的 M1，再按 ID 确认并重拉 | 确认前两次得到同一消息；确认后不再返回 M1；重复确认成功。演示页会在 WS 连接后自动确认，验证“确认前”须使用独立 HTTP 客户端 | 未验收 |
| B 仍断线时 A 再发送 M2，随后 B 重连并查询群历史 | B 在页面收到 M2 离线消息，历史包含 M1、M2；同一 `msg_id` 在当前页面会话只展示一次，翻页游标保持字符串精度 | 未验收 |
| 旧 Gin 群入口访问新团队群，同时用原有旧群做回归 | 旧开放加入、旧成员列表和旧历史入口不能绕过团队群权限；旧群原有路径仍可用 | 未验收 |

## 故障与边界（待执行）

- [ ] 在隔离环境验证 IM RPC 不可用或授权失败时，WS 群发送不进入 Kafka、不回成功 ACK；Gateway 返回相应错误。不要在正在使用的云端服务上为测试而停机。
- [ ] 验证 Push/WS 暂时不可用时的重试与离线降级；恢复后同一 `msg_id` 可重拉，演示页当前会话只展示一次。进程退出或 Kafka 重放仍可能重复投递，不能把 ACK 当作送达确认。
- [ ] 核对真实 MySQL 上的团队群历史查询索引、离线唯一约束及幂等唯一约束实际生效；核对真实 Redis 去重键与 Kafka 消费确认行为。

阶段 3 目前仍未完成真实联调。演示页去重集合只覆盖当前页面会话，刷新后重置；WS 内部 200 仅表示消息进入写队列，不能证明网络客户端已收到。离队即退出团队群的规则已经确定，但离队接口与对应群资格清理尚未实现。上述边界不能因本地测试通过而标记为已验收。
