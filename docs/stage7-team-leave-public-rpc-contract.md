# 阶段7：本人退出与状态查询 User RPC

2026-10-07。沿已确认的 A75 同步清理、固定请求键及本人显式重试，和 A77 仅非拥有者本人退出规则，本步开放 User gRPC，不改变服务归属或新增中间件。

## 本步实现与审查

1. `LeaveTeam(team_id, request_key)`通过现有登录 Token 核对启用账号，绝不从请求读取用户 ID。首次请求先在 User 事务中保存退出操作与 `leaving`，再调用 IM 专用 mTLS 清理，最后在 User 事务中保存 `left` 与完成。完成返回状态 1；清理失败返回错误并保留状态 0，本人可用同键显式重试。
2. `GetTeamLeaveOperation(team_id, request_key)`按 Token 本人及请求键读取固定操作，不要求当前仍在团队；因此退出后、清理失败或后来重新入队也能查看原操作。未知键为 `NotFound`，同键被用于另一团队为 `AlreadyExists`。响应有操作 ID、team、固定资格版本及 0/1 状态，不包含他人身份。
3. IM 清理客户端未配置时，首次请求在写入 `leaving` 前返回 `Unavailable`；已完成操作的同键重放仍成功。两方法校验正 team ID 和 1—64 位 ASCII 请求键。生成 User protobuf 代码，未手改生成结果。

验证：定向 User 测试覆盖待清理/完成状态查询、无 Token 先拒绝、跨团队同键冲突、未知键、未配置 IM 不写退出意图和完成操作重放；`go test ./... -count=1 -timeout=120s`通过。sqlmock 不能证明真实 MySQL 行锁、迁移或跨进程 mTLS。基础 Compose 没有专用证书/地址，031—033 未执行，Gateway/页面、重新入队入口、云端联调尚未完成。部署顺序须先让 Push 的团队群核权和 IM 清理服务就绪，再放开用户调用公开退出；不能将协议已注册视为线上可用。

本步修改：[协议](../rpc/user/user.proto)、[生成消息](../rpc/user/pb/user.pb.go)、[生成 gRPC](../rpc/user/pb/user_grpc.pb.go)、[处理器](../rpc/user/team_leave_rpc.go)、[协调保护](../rpc/user/team_leave_complete.go)、[测试](../rpc/user/team_leave_rpc_test.go)、[旧 API 测试客户端兼容](../api/handler_test.go)、[User说明](../rpc/user/README.md)、[决策记录](architecture-decisions.md)、[计划](project-plan.md)、[本文](stage7-team-leave-public-rpc-contract.md)。
