# 阶段7：本人退出 Gateway 与页面入口

2026-10-07。沿用户已选 A75 同步清理、固定操作和本人显式重试规则；只把已有 User RPC 接给 Gateway 和原生演示页，不改变服务边界。

## 本批实现与审查

1. Gateway 新增 `POST /api/v1/teams/{team_id}/leave`，JSON 仅 `{ "request_key": "..." }`；以及 `GET /api/v1/teams/{team_id}/leave?request_key=...`。仅转发单个 Bearer Token，拒绝请求自报用户 ID、额外字段/查询参数与非法请求键。两路都只调用 User；查询不要求当前仍在团队。响应 `data.{operation_id,team_id,generation}` 都是精确十进制字符串，`status` 为 0 待清理、1 完成；POST 只接受完成响应，不能把待清理当退出成功。
2. 退出路由给 7 秒预算，本地及 Compose Gateway 的 User RPC 客户端改为 5 秒，以覆盖已有 IM 清理最多 3 秒调用；其他 HTTP 路由仍使用原默认预算。超时或不可用仅说明结果不确定，页面保留原键并提示先查状态，不自动重试或换键。
3. 原生演示页增加本人退出、按键查询及原键重试控件。首次提交可生成随机键，之后显示并保留；旧键非空时“生成键”不覆盖。状态为待清理或完成时清当前群选择和分页状态，旧身份/团队/请求键回包不更新页面。请求键只留在输入框，请自行保存供页面刷新后查询；本步没有新增浏览器存储。

验证：Gateway 替身测试覆盖 Token/范围、精确字符串 ID、非法输入、坏成功响应及私密错误映射；页面 Node 测试覆盖结果不确定后查询/重试同键和旧身份回包隔离。`go test ./... -count=1 -timeout=120s`通过；页面 `node --test examples/chat.test.cjs` 共 167 项通过；最后新增的嵌入页面断言经定向 Go 测试通过。尚未执行 031—033 实库迁移，未配置基础 Compose 的专用 mTLS 证书，未做真实跨进程/浏览器/云端联调。上线前须让 User、IM 清理及 Push 团队群核权同时就绪；不能仅部署 Gateway 页面就允许真实退出。

本批修改：[Gateway路由](../api/main.go)、[Gateway处理器](../api/team_leave.go)、[Gateway测试](../api/team_leave_test.go)、[嵌入页面测试](../api/chat_demo_test.go)、[本地Gateway配置](../api/etc/api.yaml)、[Compose Gateway配置](../deploy/api-gateway.yaml)、[演示页](../examples/chat.html)、[页面测试](../examples/chat.test.cjs)、[Gateway说明](../api/README.md)、[架构决策](architecture-decisions.md)、[项目计划](project-plan.md)、[本文](stage7-team-leave-gateway-page-contract.md)。
