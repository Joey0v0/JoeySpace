# F6 Vue 部署与回退记录

日期：2026-10-10。目标是让 Vue 构建产物经服务器回环 `127.0.0.1:18083` 的 Nginx 入口访问，同时保留现有 Gateway、WS、旧 `/demo/chat` 与数据卷。用户审查了具体部署差异并要求继续后，第 6 步已按下述顺序执行；前期审计与候选命令保留为历史记录。F6 分支还包含 F1—F5 的后端/前端改动，不能把部署差异理解为只有一个 Nginx 容器。

## 第 6 步实际执行结果

Ask 超时分类续报（2026-10-10）：本地 `864468da190403ad68afc73353a7ce0335f23bee` 的超时/普通故障回归与全仓 Go 测试通过。Git bundle SHA-256 `4974cdc5c74a0b511c2a422b9b1d288ea5b8cfe9013835600d5002ca68163ab9` 两端核对、远端 bundle 校验通过；服务器从干净 `33defb7` 快进到 `864468d`，七份 Compose `config --quiet` 通过。只构建并切换 `agent-rpc`，旧镜像 `sha256:22c7ca24e8da51587dec89c4023f8bc239914539cece1649f201d69a01d73d0d` 记录于服务器 `/tmp/joeyspace-f6-ask-timeout-old-agent-image.txt`，新镜像 `sha256:1f96ae5fe89849b1d52eccc9a780a63b93fc5d84897b5f32c8f2a9194ae10a19` 的二进制检查与运行状态通过。服务器仓库干净、12 容器运行；本机隧道同源路由 5/5，真实 Vue Ask HTTP 200 且准确概括持久群讨论。新镜像构建后磁盘 `/opt` 剩余约 5.8 GB（85% 已用），Docker 构建缓存约 27 GB；未在本轮清理缓存。云端未注入真实模型超时，因此 HTTP 504 仅有本地错误分类与既有 Gateway 映射测试证据。

构建缓存收尾（2026-10-10）：F6 继续验收前只读确认无活动 Docker 构建、原镜像/容器和数据卷正常；仅运行 `docker builder prune --force --filter until=1h` 清理一小时未使用的构建缓存，不调用镜像、容器或数据卷清理。Docker 报回收 22.52 GB，构建缓存从 27.14 GB 降至 4.616 GB，宿主机 `/opt` 所在磁盘从 85% 已用降至 29% 已用、剩余约 27 GB。之后服务器仓库干净、12 容器运行，MySQL、Push、前端均运行，回环 `/login` HTTP 200，本机隧道同源路由 5/5；仅可能增加下次镜像重建的缓存预热时间。本操作不是 Push 停机恢复演练。

Push 维护窗口专项（2026-10-10）：用户明确确认当前时段可以短暂停止共享聊天投递。服务器代码 `864468d`、干净工作树、已演练备份、12 容器、Push 运行和 `verify-cloud.py` 完整预检先通过；随后在服务器运行 `python3 verify-cloud-resilience.py --allow-push-stop`，并加外层退出保护再尝试启动 `im-push`。脚本仅用一次性用户/团队 `2108886405953363968`：Push 停止期间的私聊消息未提前入库，恢复后 B 在线收到且历史恰好一条；B 离队后群历史、任务、通知、重入和原 WS 群发送被拒绝。脚本返回 PASS，Push 最终为 running；独立复核 12 容器、前端 `/login` 200 与隧道路由 5/5。未执行整机/数据库崩溃恢复，测试数据保留；该专项没有改代码、镜像、证书或数据卷。

第 9 步与代码审查续报（2026-10-10）：Vue 草稿跳过和直接 API 跳过同样返回 409；生产库待审草稿的 `task_request_key`/`task_id` 为 NULL，旧 SQL 用空串/零作为条件，更新 0 行。审查还发现同一用户并发重连时旧 WS 连接可在新连接之后发布在线租约。`db191c4090b950cfadd73ef5e2e329fc470b56fb` 分别让跳过条件接受未提交的 NULL、将 Hub 注册和租约发布串行化并拒绝旧连接发布。全仓 Go 测试通过；Git bundle SHA-256 `4ef1f380faba27e295817c818d97e523fcf57e96cf87acf79568f9d1e354a33e` 两端校验，服务器从 `5873cf7` 干净快进，七份 Compose 覆盖 `config --quiet` 通过。只重建 `agent-rpc` 与 `im-ws`，旧镜像 ID 分别存于 `/tmp/joeyspace-f6-task9-old-agent-image.txt`、`/tmp/joeyspace-f6-task9-old-ws-image.txt`；新镜像 ID 分别为 `sha256:22c7ca24e8da51587dec89c4023f8bc239914539cece1649f201d69a01d73d0d`、`sha256:73d32010ac126a1336c46191ba3b0fa484c8c9afe7dfc12c1cc2577a8caaff43`。12 容器运行、仓库干净、入口路由 5/5；新草稿 Vue 跳过 HTTP 200 且库内 `skipped`/无任务关联，双浏览器完整聊天回归通过。早期模型 503 仍是待观察问题。

WS 复审续报（2026-10-10）：`db191c4` 的在线发布在 Redis I/O 期间持有全局 Hub 锁，可能阻塞其他用户。`33defb7b9a8b7285effa694e00a2942a68a0bea5` 改为同用户引用计数锁，并把 Redis 写入限时 5 秒；跨用户非阻塞回归先失败后通过、全仓 Go 测试通过，复审未发现同一路径的新竞态。bundle SHA-256 `b1b1446b377fdd0de7494a58446a3939d45d935c4fb989f369bfc4700156b2b7` 两端校验，服务器从干净 `db191c4` 快进，七份 Compose `config --quiet` 通过；仅构建/切换 `im-ws`。旧 WS 镜像 `sha256:73d32010ac126a1336c46191ba3b0fa484c8c9afe7dfc12c1cc2577a8caaff43` 留在 `/tmp/joeyspace-f6-user-lock-old-ws-image.txt`，当前镜像 `sha256:d8ce184ebfa1aebb56081062f637b9a0ed985136f4df27d5cc48c106bdc62ed2`。服务器仓库干净、12 容器运行、同源路由 5/5；最终镜像下双浏览器群/私聊、未读/提及、分页、离线重登全链通过。Agent、前端、数据库和私有配置未随本次切换修改。

第 7 步续报（2026-10-10）：真实双浏览器验收发现，切换页面时旧 WS 连接可在新连接建立后无条件删除同一用户的 Redis `online:<user_id>`；Push 随后将已在线用户视为离线，群/私聊历史仍能保存。主 agent 为在线路由增加每连接随机租约，Redis 脚本原子设置路由和租约，仅允许当前租约续期或删除。全仓 `go test ./... -count=1` 通过；修复提交 `475b9857c5ef34973918aece2bbcdd4318982589` 经 Git bundle 哈希校验后在服务器快进，完整 Compose `config --quiet` 通过，只重建并切换 `im-ws` 容器。旧 WS 镜像 ID `sha256:13afb456a1e67592f0a19b1364c40b7542f2d57a4edea358236c7c54c77edb86` 保存在服务器私有 `/tmp/joeyspace-f6-ws-prefixed-old-image.txt`，新镜像 ID 为 `sha256:fd60d88b58475753f33860f6dbf23d294a5e79236789193344f969d0911da24f`。迁移与前端镜像未重新构建。修复后的双浏览器群/私聊双向在线投递及 IM 历史 ID、未读/@我、显式已读、分页和离线补拉均通过；证据见[验收清单](frontend-f6-acceptance-checklist.md)。

第 8 步续报（2026-10-10）：真实聊天来源消息在无提及成员时由 IM 省略 `mentioned_user_ids`，Vue 来源解码误拒绝；状态 PUT 成功响应仅含 `code,msg`，Vue 错误要求 `data`。分别以失败测试定位，提交 `d3ee2a0` 和 `5873cf7` 定向修复，前端 133 项测试、类型检查与生产构建通过。两份 Git bundle 校验后服务器快进至 `5873cf7db80a52edf3023d39de64313f74bd925b`；仅两次构建、切换 `frontend-web`，后端镜像、数据库和证书未改。最终前端镜像 `sha256:58cb7c446a0c85dfb6334bf3e5622033e043301617db8eb9a91521fecf0231fa`，上一镜像 `sha256:0f8c262749c268b71c742e4807b71523b20d9d16010bdfe30a20aecf6c33d18c` 的 ID 留在 `/tmp/joeyspace-f6-task8-status-old-frontend-image.txt`。12 个容器运行，回环 `/login` 200。真实两账号创建/幂等、本人列表、来源深链、三态和 409 反馈、通知 WS 提示/持久列表/显式已读均通过；一次性数据与逐项证据见[验收清单](frontend-f6-acceptance-checklist.md)。

服务器旧提交为 `c9eb885a5aaca43771470271dc49afdb00173b14`，已切换到干净的 `codex/frontend-f6-acceptance`，部署提交为 `ee4fcd89999bcf16b59ffb69ebdc26b8a94bdd5b`。本地 Git bundle 与服务器文件 SHA-256 同为 `d399f62bcc8a2928fef0457a0a08877d5ee2002773acf18c96224f0dab7d8a68`，双方 `git bundle verify` 通过；没有传输私有 `.env`、YAML、证书或密钥。服务器原有五份 Compose 覆盖加 mentions/frontend 覆盖在同一 `deploy` 项目中 `config --quiet` 通过。

迁移前再次核对已演练的 SQL 备份 SHA-256 为 `c9f9039247f726ca887d927825b1862cd69dd83e55c4ec8e404d3515b9e98490`；036 和 037 依次执行退出码均为 0。生产库中 `im_group_message_mentions` 为 1 张表；`idx_tasks_assignee_team_status_due` 的五列依次为 `assignee_id,team_id,status,due_at_unix_ms,id`。未对旧数据卷做删除或整库恢复。

服务器构建 `user-rpc`、`im-rpc`、`task-rpc`、`api-gateway`、`im-ws`、`im-push`、`agent-rpc`、`frontend-web` 八个镜像；八个构建完成标记及镜像 ID 均已核对。七个后端服务按依赖顺序逐个 `--no-deps --no-build --force-recreate` 更新，每个容器都通过运行状态检查；`frontend-web` 先经 `nginx -t`，再启动。旧 `im-api` 与 MySQL、Redis、Kafka 保留。最终 12 个预期容器均运行，18083 仅绑定 `127.0.0.1`；服务器仓库仍干净。

服务器回环 `/login`、`/messages`、`/tasks` 均为 200；缺失资源 404，未知 API 404，GET `/ws-ticket` 405，旧 `/demo/chat` 200。本机经 SSH 隧道运行 `node deploy/verify-frontend-routes.cjs`，5/5 通过；真实 Chrome 运行 `node deploy/verify-vue-browser.cjs`，登录、消息刷新、本人群和任务深链、同源 API/WS 通过，观察到 43 次 API/票据请求及 4 次 WS 101 升级。独立 WS 票据握手验证错误 Origin 403、无效票据 401、有效票据 101、重放 401。完整逐项结果见[Vue 验收清单](frontend-f6-acceptance-checklist.md)；双账号聊天、任务通知和 AI 仍待任务 7—9。首次 Chrome 运行受本机沙箱限制，获准的非沙箱复测通过，未改产品代码。

第 6 步的回退基线为旧提交 `c9eb885a5aaca43771470271dc49afdb00173b14`；当次更新前七个后端容器的镜像 ID 保存在服务器私有 `/tmp/joeyspace-f6-old-images.txt`。SQL 备份位于 `/opt/joeyspace-backups/go_im-20261010T060712Z.sql`；数据库对象默认向前保留，不自动删除。回退尚未触发或演练实际服务切换，不能把备份恢复演练等同于部署回退演练。第 6 步结束时服务器代码为 `ee4fcd8`；后续更新版本见本节开头的续报。

## 1. 部署前快照（历史）

| 项目 | 当时本地事实 | 目标服务器部署前状态 |
| --- | --- | --- |
| 源代码 | `codex/frontend-f6-acceptance`；当前 F6 提交以本地 `git rev-parse HEAD` 为准 | 用户在服务器核对：`main` / `c9eb885a5aaca43771470271dc49afdb00173b14`，工作树无未提交条目；本地 Git 确认该提交是 F6 的祖先 |
| 入口 | 新增 `Dockerfile.frontend`、`frontend-nginx.conf`、`docker-compose.frontend.yaml`；回环 18083 | 用户回传 `ss` 结果中 18083 无监听；直接 SSH 核对 Docker Server `29.8.2` |
| Compose | 拟用基础 + bot + trigger + notifications + team-leave + mentions + frontend，`--profile agent` | 项目名 `deploy`；实际覆盖为基础 + bot + trigger + notifications + team-leave，共 11 个运行容器；mentions/frontend 尚未启用 |
| 数据 | 本地 036 建 `im_group_message_mentions`；037 给 `tasks` 增复合索引 | 运行库缺 036 表和 037 索引，035 已存在，23 张表均为 InnoDB。数据卷 `deploy_mysql_data`，MySQL 目录约 200 MB，宿主机可用约 19 GB；已生成私有 SQL 备份并在隔离 MySQL 8.0 中恢复 23 张表成功，生产库未写入 |
| 私有配置 | 本地隔离 worktree 无 `deploy/.env`、`deploy/docker-config.local.yaml`；未读取凭证 | 原 12 个角色证书目录完整，三项 Agent/通知私有开关为 `true`；F11 两份独立证书已由原 CA 签发、验证并加入私有 `.env`，有效期至 2027-10-10 UTC。`.env` 为 600；未读取或输出密钥内容 |
| 运行验证 | Vue 构建、Node 测试与 Compose 静态解析另记本批结果 | 镜像、Nginx、API/WS 和真实浏览器：**NOT RUN** |

第 6 步只读审计起点（2026-10-10）：本机没有 SSH Host 别名或现成隧道；本地参考差异不能代替服务器版本比较。随后取得服务器 SHA 并在本地确认它是 F6 分支祖先。服务端 `c9eb885` 到当前 F6 提交的实际差异覆盖 Gateway、User/IM/Task RPC、Push/WS、Vue、Compose 和 036/037；没有 `rpc/agent/` 代码差异。不能只重建前端就宣称整链可用。

连接补充（2026-10-10）：用户已提供目标 SSH 地址；服务器命令确认仓库目录为 `/opt/JoeySpace`。最初免交互 SSH 认证失败。用户随后把本机新建的 JoeySpace 专用公钥加入服务器授权列表；本机私钥留在用户电脑的 SSH 目录。修正本机私钥意外设置的口令后，严格校验主机密钥的免交互 SSH `pwd` 返回 `/root`，现可由主 agent 直接运行只读审计。公钥授权由用户执行；未传送密码或私钥。

用户只读回传已确认 `/opt/JoeySpace`、服务器 SHA、干净工作树、活动 Compose 项目/覆盖及容器状态；`18083` 无监听。有效单行 SQL 查询确认 035 索引已存在、036 表和 037 索引均不存在；`go_im` 的 23 张表均为 InnoDB；F11 两个证书目录变量均未设置。运行库使用 `deploy_mysql_data`，目录约 200 MB，宿主机可用约 19 GB。此前多行命令被终端合并及一次错误转义 SQL 的失败输出不作为环境事实。随后已直接完成备份和隔离恢复演练；仍待证书签发与正式部署审查，不执行同步或重建。

服务器审计前不得根据仓库的 `init.sql` 推断旧数据卷已升级，也不得运行 `down -v`。以下所有命令中的 `<SERVER>`、`<REPO>`、`<OLD_SHA>`、`<F6_SHA>`、`<BACKUP>` 必须在只读核对后替换为审查过的真实值；这些占位符未填写时，命令不可执行。

## 2. 部署前只读核对

在服务器已存在的仓库目录 `<REPO>` 中记录，不输出 `.env`、完整 `compose config`、Token 或私钥：

```sh
cd /opt/JoeySpace
pwd
git rev-parse HEAD
git branch --show-current
git status --short
docker compose ls
docker ps --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}'
docker compose --env-file deploy/.env -f deploy/docker-compose.yaml ps --all
docker version --format '{{.Server.Version}}'
ss -ltn '( sport = :18083 )'
```

可回传的最小脱敏结果是上述命令输出、`information_schema` 的表/索引结果，以及“备份已存在且可恢复、证书目录与开关齐备”的逐项真假；不要回传 `.env`、私有 YAML 正文、数据库密码、Token、证书或密钥。`docker compose ls` 与基础文件的 `ps` 若对应不同项目，应以实际运行容器标签核对项目名后重写后续命令。

按现有 Compose 项目名和数据卷核对正在使用的 MySQL 容器，先确认备份方式和可恢复性。使用该容器内已有凭证做只读结构查询；只有表/索引、前置迁移和备份结果均确认后，才决定是否执行 036/037：

```sh
docker compose --env-file deploy/.env -f deploy/docker-compose.yaml exec -T mysql \
  sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot --batch --skip-column-names information_schema' <<'SQL'
SELECT TABLE_NAME FROM TABLES
WHERE TABLE_SCHEMA='go_im' AND TABLE_NAME='im_group_message_mentions';
SELECT INDEX_NAME,COLUMN_NAME,SEQ_IN_INDEX FROM STATISTICS
WHERE TABLE_SCHEMA='go_im' AND TABLE_NAME='tasks'
  AND INDEX_NAME='idx_tasks_assignee_team_status_due'
ORDER BY SEQ_IN_INDEX;
SQL
```

035 索引和 030、033、034 对应表已从只读输出确认；直接 SSH 又核对到 031 的 `membership_state`/`generation` 列和 032 表。Agent/通知链的全部实际迁移状态仍须按对象核对，不能仅凭表名认定全链可运行。现有 12 个角色的证书目录文件齐全，Agent/通知三项私有 YAML 开关均为 `true`。F11 已复用原 CA，在仓库外签发两份不同私钥：IM 服务端 DNS SAN `im.go-im.internal`、Push 客户端 DNS SAN `push.go-im.internal`，用途/CA/主机名验证通过，到期日为 2027-10-10 UTC；两个变量在 `.env` 中各出现一次，路径和所需文件存在，原仓库仍干净。尚未叠加新覆盖或重启容器。拟新增的 mentions/frontend 覆盖已与当前五份覆盖列表比对，启用仍须先执行缺失迁移和审查代码发布顺序。

## 3. 拟执行的同步、备份与迁移（用户审查后）

### 3.1 先备份并隔离恢复（2026-10-10 已执行）

在服务器仓库与数据卷之外创建 `/opt/joeyspace-backups/go_im-20261010T060712Z.sql`，权限为仅 root 可读，大小 73,974 字节，SHA-256 为 `c9f9039247f726ca887d927825b1862cd69dd83e55c4ec8e404d3515b9e98490`。转储退出码 0；未展示 SQL 内容。以下命令是本次操作的可复核形式，之后使用仍须重新核对环境和文件名。

```sh
cd /opt/JoeySpace/deploy || exit 1
umask 077
install -d -m 700 /opt/joeyspace-backups
backup=/opt/joeyspace-backups/go_im-$(date -u +%Y%m%dT%H%M%SZ).sql
docker exec deploy-mysql-1 sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -uroot --single-transaction --quick --routines --triggers --events --set-gtid-purged=OFF --databases go_im' > "$backup.partial" && test -s "$backup.partial" && mv "$backup.partial" "$backup" && sha256sum "$backup" && ls -lh "$backup"
```

只读引擎查询已确认当前 23 张表均为 InnoDB；`--single-transaction` 仍要求备份期间没有 DDL 才能保证在线一致性。隔离恢复使用临时 MySQL 8.0 容器，没有映射端口、连接生产 Compose 网络或挂载生产数据卷。首次演练因 `mysqladmin ping` 早于 root SQL 登录可用而返回 1045；临时容器已清理，备份未变。改为已认证 `SELECT 1` 就绪判断后，同一备份导入退出码 0，恢复库有 23 张表，`messages`、`tasks` 和 `im_group_message_reads` 均存在；临时容器已清理。

```sh
test -s "$backup" || exit 1
docker container inspect joeyspace-f6-restore-check >/dev/null 2>&1 && { echo 'temporary_name_in_use'; exit 1; }
restore_pw=$(openssl rand -hex 24)
docker run -d --name joeyspace-f6-restore-check --network none -e MYSQL_ROOT_PASSWORD="$restore_pw" mysql:8.0
for i in $(seq 1 60); do docker exec joeyspace-f6-restore-check sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -N -e "SELECT 1"' >/dev/null 2>&1 && break; sleep 2; done
docker exec joeyspace-f6-restore-check sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -N -e "SELECT 1"'
docker exec -i joeyspace-f6-restore-check sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot' < "$backup"
docker exec joeyspace-f6-restore-check sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot -N -e "SHOW TABLES" go_im' | grep -E '^(messages|tasks|im_group_message_reads)$' | wc -l | grep '^3$'
docker rm -f joeyspace-f6-restore-check
unset restore_pw
```

恢复后再次只读确认：生产仓库仍为干净的 `c9eb885`，11 个原有容器运行，临时容器不存在，备份文件仍为 73,974 字节。恢复演练不能代替整机/数据库故障恢复预案；迁移回退若要恢复整库，会覆盖备份之后的新写入，需另行确定停写窗口。备份文件只保存在服务器私有目录，不发送 SQL 文件。

本地冻结 `<F6_SHA>` 后，可建立仅含 Git 跟踪文件的 bundle；它不包含被忽略的私有 `.env`、YAML 和证书。服务器工作树必须干净、旧提交 `<OLD_SHA>` 已记录，才在服务器获取 bundle 并切换到独立 F6 分支。传输目标与 SSH 身份以目标环境实际值填写：

当前精确回退代码基线为 `c9eb885a5aaca43771470271dc49afdb00173b14`；本地 F6 交付提交以部署前 `git rev-parse HEAD` 冻结。主 agent 已有经用户授权的专用 SSH 公钥，传输使用该身份。服务器工作树最后一次只读检查仍干净；真正切换前再次检查，不强制覆盖变更。

```sh
git bundle create joeyspace-f6.bundle codex/frontend-f6-acceptance
scp joeyspace-f6.bundle <SERVER>:/tmp/joeyspace-f6.bundle
ssh <SERVER> 'cd <REPO> && git status --short && git fetch /tmp/joeyspace-f6.bundle codex/frontend-f6-acceptance:refs/heads/codex/frontend-f6-acceptance && git switch codex/frontend-f6-acceptance && git rev-parse HEAD'
```

切换前要先确认 bundle 与目标 Git 历史兼容，且不会覆盖未提交改动。若目标不是 Git 仓库或工作树不干净，停止并另定同步方法；不通过强制重置处理。先用数据库运维既定方式取得可恢复备份，记录备份文件 `<BACKUP>`、卷名、大小与恢复演练结果；未拿到这些证据不执行迁移。

以下命令只适用于**已确认该迁移尚未应用**且前置对象齐备的同一 `go_im` 库。037 会改任务表索引，按表大小选维护窗口。已存在对象时跳过，失败时停止，不盲目重跑：

```sh
cd <REPO>/deploy
docker compose --env-file .env -f docker-compose.yaml exec -T mysql \
  sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot go_im' \
  < mysql/migrations/036_im_group_message_mentions.sql
docker compose --env-file .env -f docker-compose.yaml exec -T mysql \
  sh -c 'MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot go_im' \
  < mysql/migrations/037_task_personal_indexes.sql
```

## 4. 部署前候选 Compose、验证与回退命令（历史）

服务器旧提交到本地 F6 分支的代码差异要求重建并按依赖顺序更新 `user-rpc`、`im-rpc`、`task-rpc`、`api-gateway`、`im-ws`、`im-push`，最后启动 `frontend-web`。`agent-rpc` 自身源码未变，但生产包引用本次变化的 IM/Task/User 生成协议与共享模型；为保持整套二进制对应同一提交，也重建并检查 `agent-rpc`。旧 `im-api` 没有本轮需要的新入口，先保留为原有诊断服务，不能把它的旧版本用于 F6 验收。旧覆盖列表为基础 + bot + trigger + notifications + team-leave；拟新增 mentions 和 frontend，保留原有五份及同一 `deploy` 项目，不更换 MySQL 卷。先升级 User，再 IM/Task，随后 Gateway/WS，最后 Push、Agent 和正式页；每步检查容器状态，异常立即停下并执行相应回退。不对 036/037 做自动 `DROP`。

证书准备已完成：复用 `/opt/joeyspace-secrets` 现有 CA，只签发两个**新私钥**，目录分别为 `im-mention` 与 `push-im-mention`；前者为 `im.go-im.internal` 的服务端证书，后者为 `push.go-im.internal` 的客户端证书，均包含各自 DNS SAN 与用途，并用原 CA 验证。新目录/私钥采用仅 root 可读权限，私有 `.env` 只追加 `IM_MENTION_CERT_DIR` 与 `PUSH_IM_MENTION_CERT_DIR` 两项路径；未修改原 12 份证书或 CA 私钥。签发和路径验证退出码均为 0；`config --quiet` 尚未对新覆盖在目标服务器执行，待代码同步后核对。

以下函数是目标覆盖列表的**候选值**；先与第 2 节真实列表比较，核实每份覆盖所需私有证书、开关和模型预算，再运行。`config --quiet` 不打印展开后的凭证。已有环境若尚未启用任一覆盖，不能为了 F6 直接套用整组命令。

```sh
cd <REPO>/deploy
compose() {
  docker compose --env-file .env --profile agent \
    -f docker-compose.yaml -f docker-compose.bot.yaml \
    -f docker-compose.trigger.yaml -f docker-compose.notifications.yaml \
    -f docker-compose.team-leave.yaml -f docker-compose.mentions.yaml \
    -f docker-compose.frontend.yaml "$@"
}
compose config --quiet
compose build frontend-web
compose up -d --no-deps im-ws
compose up -d --no-deps frontend-web
compose ps frontend-web im-ws api-gateway
```

不能仅重建前端宣称整链可用。从开发者电脑建立 `ssh -N -L 18083:127.0.0.1:18083 <SERVER>`；在开发者电脑的 F6 仓库根目录运行 `node deploy/verify-frontend-routes.cjs`，再运行 `node deploy/verify-vue-browser.cjs` 并填写[逐项清单](frontend-f6-acceptance-checklist.md)。这样无需假定服务器安装 Node。有效/重放 WS 票据、双账号业务和持久事实仍按清单人工核对。任一基础路由或权限失败，停止后续业务验收。

回退入口时，在**同一个已核实的 Compose 项目和完整覆盖列表**下先移除新增服务，再切回旧提交：

```sh
cd <REPO>/deploy
compose stop frontend-web
compose rm -f frontend-web
cd ..
git switch --detach <OLD_SHA>
```

随后用部署前记录的旧覆盖列表与项目名重建**本次实际更新过**的旧版服务，并检查 `compose ps`、旧 `/demo/chat` 与权威 API。旧覆盖列表、服务集及精确重建命令必须在第 2 节审计后填写，未填写不能开始部署。不得删除卷或私有配置。数据库 036/037 的向前兼容对象默认保留，不自动 `DROP`；如必须恢复数据库，按 `<BACKUP>` 的已演练恢复程序在维护窗口单独处理，先确认这会覆盖备份之后的写入。执行后记录旧服务版本、健康状态和回环端口结果，不把代码回退等同于数据回退。

## 5. 本地检查与审查门槛

本批本地结果（2026-10-10）：`frontend/npm test` **132/132 PASS**，`frontend/npm run build` **PASS**，`go test ./... -count=1` **PASS**，使用占位环境合并基础、bot、trigger、notifications、team-leave、mentions、frontend 覆盖的 `docker compose config --quiet` **PASS**。`git diff --check` 在最终提交前复核。占位环境只验证 YAML 合并，不证明目标私有证书或配置可用。

以上是任务 1—5 结束时的本地门槛：当时 Windows Docker daemon 不可用，镜像与真实路由/WS 尚未验证。任务 6 的服务器构建、`nginx -t`、迁移、切换和真实入口结果现已记录在本文开头；两次时间点不可混用。
