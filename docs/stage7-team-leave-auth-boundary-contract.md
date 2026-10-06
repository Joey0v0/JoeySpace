# 阶段7：本人退出意图的鉴别边界

2026-10-06，沿用户确认的A75/A77规则，在`codex/stage7-team-leave-intent`继续一个小步：让User包内调用方只能从现有登录Token获得退出者身份，再进入前一批的原子退出事务。这样未来公开入口不必接受请求自报的用户ID，也不会把伪造metadata当作身份。

## 本步实现与审查

1. 在`rpc/user/team_leave_intent.go`新增`userServer.beginOwnTeamLeaveIntent`。它调用现有`GetMyInfo`，复用Token验签、账户启用检查及数据库错误处理，然后只用已验证的本人ID调用`beginTeamLeaveIntent`。未改已有事务、表、协议或依赖。
2. 在`rpc/user/team_leave_intent_test.go`验证首次请求和同键重试均使用Token中的42，而非伪造metadata中的99；无Token、仅自报ID及无效Token均不能进入写事务。测试用SQL替身，不证明真实MySQL锁或Token在部署配置中的密钥正确性。
3. root运行`go test ./rpc/user -run 'TestBeginOwnTeamLeaveIntent|TestBeginTeamLeaveIntent' -count=1 -timeout=90s`和`go test ./... -count=1 -timeout=90s`，均通过。更新`docs/project-plan.md`、`docs/architecture-decisions.md`、`docs/stage7-acceptance.md`，记录先把鉴别边界准备好、暂不注册可调用RPC的顺序。

调用链当前是**包内**`beginOwnTeamLeaveIntent` → `GetMyInfo`验证本人及账户状态 → `beginTeamLeaveIntent`保存User撤权与033操作。没有`user.proto`方法、Gateway路由或页面按钮；生产流量尚不能触发退出。原因是IM清理和Push当前资格核对未接线，过早开放写入可能使User已撤权而残留群成员仍被Push投递。下一步优先完成受控IM清理与Push核权的接线，再开放本人可调用的退出与显式重试入口。

实际修改文件：`rpc/user/team_leave_intent.go`、`rpc/user/team_leave_intent_test.go`、`docs/stage7-team-leave-auth-boundary-contract.md`、`docs/project-plan.md`、`docs/architecture-decisions.md`、`docs/stage7-acceptance.md`。未运行031—033迁移、真实MySQL、容器或云端；不标A16或阶段7完成。
