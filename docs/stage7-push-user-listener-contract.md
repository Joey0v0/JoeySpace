# 阶段7：User的Push专用mTLS监听

2026-10-06。沿用户已确认的A76，给[受限资格协议](stage7-push-user-eligibility-contract.md)接独立、默认关闭的User服务端口；本步仍不修改Push消息投递或开放退出入口。

## 本步实现与审查

1. 新增`USER_PUSH_LISTEN_ON`及三项`USER_PUSH_TLS_*_FILE`配置，四项全空即关闭，启用时必须完整、要求`-profile`，并与普通User及Agent Trigger监听使用不同端口。证书在监听前加载；TLS 1.3且客户端证书的精确DNS SAN固定为`push.go-im.internal`，不接受仅同CA的IM/Agent身份。
2. Push端口只注册`UserPush`，限制4KiB请求；普通User和Trigger端口均未注册它。启动顺序为先准备数据库与各监听，再启动普通User RPC；Push准备失败会关闭已准备的Trigger监听及其他资源。任一专用监听异常退出，停止普通User RPC，并在defer关闭资源后以非零状态退出，避免被进程监管误判为成功。
3. 定向测试覆盖全空/缺项/非法配置、端口冲突、无数据库/坏证书/占用端口、重复Stop、真实本机mTLS下Push证书成功及IM证书/明文拒绝、两种专用监听同时运行的服务隔离和异常退出协调。`go test ./rpc/user -run 'TestUserPush|TestUserPrivateListener|TestUserTrigger' -count=1 -timeout=90s`及全仓`go test ./... -count=1 -timeout=120s`结果见本步汇报。

边界：基础Compose尚未提供Push专用证书/监听变量，Push消费者尚未调用此端口，IM群成员及关闭版本仍须在投递前核验；公开退出仍不开放。031—033、真实MySQL/并发、生产证书与容器/云端联调待验收，阶段7和A16未完成。

实际修改：[监听实现](../rpc/user/push_listener.go)、[监听测试](../rpc/user/push_listener_test.go)、[User启动](../rpc/user/main.go)、[共用退出协调](../rpc/user/trigger_listener.go)、[本记录](stage7-push-user-listener-contract.md)、[决策](architecture-decisions.md)、[计划](project-plan.md)、[User说明](../rpc/user/README.md)、[部署说明](../deploy/README.md)。
