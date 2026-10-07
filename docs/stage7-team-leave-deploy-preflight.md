# 阶段7：团队退出链路部署前核对

2026-10-07。本步沿已确认的 A75 同步 User→IM 清理、A76 Push→User 核权，准备显式启用的 [Compose 覆盖](../deploy/docker-compose.team-leave.yaml)。基础 Compose 保持默认关闭；没有连接数据库、生成真实证书或启动容器。

## 本步实现与审查

1. 覆盖文件给 IM 的独立清理监听使用容器内 `9008`，User 以 `im-rpc:9008` 访问；给 User 的独立 Push 资格监听使用容器内 `9009`，Push 以 `user-rpc:9009` 访问。与普通 RPC、Bot、Trigger 端口分离，不发布宿主机端口。`user-rpc`、`im-rpc`、`im-push`分别只读挂载所需私有证书；User 持有两套不同角色的证书，不复用私钥。缺少任一目录时 Compose 插值应直接报错，bind 不自动创建宿主机目录。
2. 已有 MySQL 卷先核对 `001_teams.sql` 已落地，再依次执行一次 [031](../deploy/mysql/migrations/031_team_membership_lifecycle.sql)、[032](../deploy/mysql/migrations/032_im_team_group_fences.sql)、[033](../deploy/mysql/migrations/033_user_team_leave_operations.sql)。031 是 `ALTER TABLE`，不可重跑；033 也只执行一次。仅替换 `init.sql` 不会升级已有数据卷。执行前做数据库备份并确认当前表结构，不能在未知迁移状态下盲跑。
3. 四个私有证书目录分别对应 User 清理客户端、IM 清理服务端、User Push 资格服务端、Push 资格客户端；各含 `cert.pem`、`key.pem`、`ca.pem`。IM 服务端证书的 DNS SAN 为 `im.go-im.internal`，User 清理客户端为 `user.go-im.internal`；User Push 服务端为 `user.go-im.internal`，Push 客户端为 `push.go-im.internal`。两端 CA 相互信任，证书有效期及 ServerAuth／ClientAuth 用途匹配；测试临时私钥不可用于部署或提交 Git。
4. 最终启用时，先用 `docker compose --env-file .env -f docker-compose.yaml -f docker-compose.team-leave.yaml config --quiet` 核对合并配置；准备全部迁移和证书后，先升级 User、IM 和 Push 并确认专用监听及核权调用，再升级公开退出的 Gateway。Push 缺配置会让团队群消息继续失败重试，User→IM 清理不可用时退出操作可停在待清理，须由本人查询并同键重试。真实联调需覆盖本人退出、清理失败恢复、离队不收消息、重入版本递增和旧群需自行 Join。

在已授权的数据库连接中，可先用只读查询核对旧库状态，再决定是否执行相应迁移；执行后复查预期表、字段和约束，不以“SQL文件存在”代替数据库状态：

```sql
SELECT TABLE_NAME FROM information_schema.TABLES
WHERE TABLE_SCHEMA = 'go_im' AND TABLE_NAME IN
('teams', 'team_members', 'im_team_group_fences', 'user_team_leave_operations');
SELECT COLUMN_NAME, COLUMN_DEFAULT FROM information_schema.COLUMNS
WHERE TABLE_SCHEMA = 'go_im' AND TABLE_NAME = 'team_members'
AND COLUMN_NAME IN ('membership_state', 'generation');
SELECT CONSTRAINT_NAME FROM information_schema.TABLE_CONSTRAINTS
WHERE TABLE_SCHEMA = 'go_im' AND TABLE_NAME IN
('team_members', 'im_team_group_fences', 'user_team_leave_operations')
AND CONSTRAINT_TYPE = 'CHECK';
```

完成031—033后应有上面四张表，`membership_state`默认0、`generation`默认1，以及各迁移定义的检查约束。若已有部分结构或约束不一致，先定位历史迁移状态，不直接重跑031/033。

本机已用 PyYAML 解析覆盖文件，核对四个端点值、三个服务名、四个私有目录变量及证书路径均落在对应只读挂载下、没有 `ports`，且关闭自动建目录；静态比对确认031字段/约束存在于新库init，032/033建表定义与init一致。未运行 `docker compose config`：当前机器没有 Docker 命令；也没有真实证书、MySQL 迁移或跨进程联调。此静态检查不能代替 Compose 插值、TLS 握手或实际数据库验证，A16/阶段7仍未完成。

## 本地组合故障验证（2026-10-07）

新增 [User退出与Push核权组合测试](../rpc/user/team_leave_push_flow_test.go)：用现有 SQL 替身表示已持久撤权的待清理操作，让 IM 清理第一次失败，检查本人仍可查到待清理状态，生产 User Push 专用监听与生产 Push 客户端通过本机双向 TLS 核权时拒绝投递资格；本人以同一请求键重试清理成功后仍拒绝，User 查询出错时则让 Push 获得可重试错误。现有 `TestAddTeamMemberRejoinsOnlyAfterCompletedCleanup`、`TestTeamGroupGenerationAcrossUserAndIMTCP`、`TestTeamDeliveryRequiresBothCurrentQualificationsBeforeOffline` 分别验证完成后版本递增、IM 旧群资格不会自动恢复、Push 仍需两侧资格。本步没有新增服务边界或选型。

`go test ./rpc/user ./internal/push ./rpc/im -count=1` 已通过。SQL/IM 故障仍由替身控制，测试使用临时证书和本机连接；尚未验证真实 MySQL 事务、实际容器证书、Push→WS 在线投递或云端部署。最终统一验收仍需按上节顺序执行迁移、Compose 合并检查及跨进程故障场景。

部署准备那一步的文件：[可选覆盖](../deploy/docker-compose.team-leave.yaml)、[环境变量示例](../deploy/.env.example)、[部署说明](../deploy/README.md)、[决策记录](architecture-decisions.md)、[项目计划](project-plan.md)、[本文](stage7-team-leave-deploy-preflight.md)。本次组合验证仅改测试、本文和项目计划。
