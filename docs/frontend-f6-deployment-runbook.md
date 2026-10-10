# F6 Vue 部署前审计与回退操作单（待审查，未执行）

日期：2026-10-10。目标是让 Vue 构建产物经服务器回环 `127.0.0.1:18083` 的 Nginx 入口访问，同时保留现有 Gateway、WS、旧 `/demo/chat` 与数据卷。**本文是拟执行步骤；服务器同步、数据库写入和容器重建均未获本轮执行确认，也未执行。** F6 分支还包含 F1—F5 的后端/前端改动，不能把部署差异理解为只有一个 Nginx 容器。

## 1. 已核实与待核实

| 项目 | 本地事实 | 目标服务器状态 |
| --- | --- | --- |
| 源代码 | `codex/frontend-f6-acceptance`；已集成代码提交 `8dc4eb6`，执行前以 `git rev-parse HEAD` 记录最终文档提交 | 当前 SHA、工作树是否干净：**BLOCKED，未连接** |
| 入口 | 新增 `Dockerfile.frontend`、`frontend-nginx.conf`、`docker-compose.frontend.yaml`；回环 18083 | 18083 是否空闲、Docker daemon/版本：**BLOCKED** |
| Compose | 拟用基础 + bot + trigger + notifications + team-leave + mentions + frontend，`--profile agent` | 实际项目名、已启用覆盖及容器状态：**BLOCKED** |
| 数据 | 本地 036 建 `im_group_message_mentions`；037 给 `tasks` 增复合索引 | 已有 `go_im` 数据卷、备份、036/037 与所有前置迁移：**BLOCKED** |
| 私有配置 | 本地隔离 worktree 无 `deploy/.env`、`deploy/docker-config.local.yaml`；未读取凭证 | 私有 `.env`、YAML、F11 两份专用证书和模型额度：**BLOCKED** |
| 运行验证 | Vue 构建、Node 测试与 Compose 静态解析另记本批结果 | 镜像、Nginx、API/WS 和真实浏览器：**NOT RUN** |

第 6 步只读审计更新（2026-10-10）：本机没有 SSH Host 别名，仓库文档没有可执行的服务器地址或仓库绝对路径；`127.0.0.1:18083`、`18082`、`18081` 均无监听。尚无目标环境连接，因此本节右栏继续为 `BLOCKED/NOT RUN`。本地 `main` 到 F6 分支的差异覆盖 `api/`、`rpc/user/`、`rpc/im/`、`rpc/task/`、`rpc/agent/`、`internal/push/`、`internal/ws/`、`cmd/push/`、`cmd/ws/`，并含 036/037 迁移；这只是**本地参考差异**，目标服务器旧 SHA 未知，不能据此直接生成服务重建清单。当前 F6 SHA：`440ae24711cca0ee3573ceccf74843177338e3a5`。

服务器审计前不得根据仓库的 `init.sql` 推断旧数据卷已升级，也不得运行 `down -v`。以下所有命令中的 `<SERVER>`、`<REPO>`、`<OLD_SHA>`、`<F6_SHA>`、`<BACKUP>` 必须在只读核对后替换为审查过的真实值；这些占位符未填写时，命令不可执行。

## 2. 部署前只读核对

在服务器已存在的仓库目录 `<REPO>` 中记录，不输出 `.env`、完整 `compose config`、Token 或私钥：

```sh
cd <REPO>
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

还须确认 030、031—035 和 Agent/通知链的实际迁移状态，不能只凭 036/037 的对象存在就认定全链可运行。核对私有目录是否存在、证书用途/DNS SAN/有效期及 `.env` 追加 Origin；只记录布尔结果和到期日，不展示密钥。若目标当前覆盖列表与拟用列表不同，先改写本操作单并重新审查。

## 3. 拟执行的同步、备份与迁移（用户审查后）

本地冻结 `<F6_SHA>` 后，可建立仅含 Git 跟踪文件的 bundle；它不包含被忽略的私有 `.env`、YAML 和证书。服务器工作树必须干净、旧提交 `<OLD_SHA>` 已记录，才在服务器获取 bundle 并切换到独立 F6 分支。传输目标与 SSH 身份以目标环境实际值填写：

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

## 4. 拟执行的 Compose、验证与回退（用户审查后）

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

如果 F1—F5 后端新代码尚未在目标环境，需在变更清单中逐项列出需构建/重启的 Gateway、IM、Push、Task、Agent 等服务及依赖顺序，完成迁移/证书核对后再单独执行；不能仅重建前端宣称整链可用。从开发者电脑建立 `ssh -N -L 18083:127.0.0.1:18083 <SERVER>`；在开发者电脑的 F6 仓库根目录运行 `node deploy/verify-frontend-routes.cjs`，再运行 `node deploy/verify-vue-browser.cjs` 并填写[逐项清单](frontend-f6-acceptance-checklist.md)。这样无需假定服务器安装 Node。有效/重放 WS 票据、双账号业务和持久事实仍按清单人工核对。任一基础路由或权限失败，停止后续业务验收。

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

Docker daemon 在当前 Windows 环境不可用，`docker build`、`nginx -t` 和真实路由/WS 验证均为 **BLOCKED**，不得标记正式入口已验收。当前未执行云端同步、迁移、容器重建或业务写入。用户审查本操作单及最终差异后，再进入任务 6。
