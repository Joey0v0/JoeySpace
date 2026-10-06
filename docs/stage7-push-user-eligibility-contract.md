# 阶段7：Push查询User当前团队资格的受限协议

2026-10-06。沿用户已选择的A76 Push→User独立mTLS核权方案，本步只准备受限`UserPush.CheckPushTeamMember`协议与User处理器，不注册监听、不接Push消费者，也不开放退出操作。

## 本步实现与审查

1. 新增`push.proto`，请求仅含`team_id`、`user_id`；成功响应仅回显两项范围和当前正`generation`。此方法只判断User拥有的团队资格，不授予IM群访问权，不返回用户资料、角色或Token。
2. User处理器先要求精确`push.go-im.internal`的已验证TLS客户端身份，再检查正ID；查询启用账号及`team_members.membership_state=active`，并要求唯一、正版本结果。缺资格返回`PermissionDenied`，SQL故障、坏行或重复行返回`Unavailable`，不将故障误判为授权或泄漏SQL文本。
3. 定向测试覆盖有效范围、未登录元数据伪装、其他服务证书、非法ID、离队/停用、坏版本、重复行及SQL故障。运行`go test ./rpc/user -run TestPushTeamMember -count=1 -timeout=90s`和全仓`go test ./... -count=1 -timeout=120s`，结果见本步汇报。

边界：本步没有把`UserPush`注册到普通User或UserTrigger端口；尚无专用mTLS监听和Push客户端，所以生产进程不能调用此协议。后续Push必须另外核对IM群成员及持久关闭版本，且同时保护在线与离线保存；临时故障应重试而非放行。031—033及真实MySQL、真实证书双进程、Kafka/WS/浏览器/云端均未验收，A16/阶段7不标完成。

本步实际文件：[协议](../rpc/user/push.proto)、[生成消息](../rpc/user/pb/push.pb.go)、[生成RPC](../rpc/user/pb/push_grpc.pb.go)、[处理器](../rpc/user/push_team.go)、[测试](../rpc/user/push_team_test.go)、[本记录](stage7-push-user-eligibility-contract.md)、[架构决策](architecture-decisions.md)、[计划](project-plan.md)、[User说明](../rpc/user/README.md)、[部署说明](../deploy/README.md)。
