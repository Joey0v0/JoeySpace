# 阶段7：Push团队群在线与离线投递资格保护

2026-10-06。沿用户已选A76，将User专用资格RPC与IM当前群成员/永久关闭记录核验接入Push的团队群投递路径；不改变旧非团队群、单聊或任务通知路径。

## 三个小步骤与审查

1. IM数据库仓储增加`CheckTeamGroupMemberGeneration(group, team, user, generation)`。在User回显正版本后，以一条SQL重新确认当前群成员、群仍属于该team、`closed_through_generation < generation`；无成员或已关闭返回明确拒绝，坏行/SQL故障返回错误。团队ID由持久`groups.team_id`读取，不信任Kafka自报。
2. Push增加可选的专用User mTLS客户端：完整设置`PUSH_USER_RPC_ADDR`、`PUSH_USER_TLS_CERT_FILE`、`PUSH_USER_TLS_KEY_FILE`、`PUSH_USER_TLS_CA_FILE`后启用，精确验证User服务端证书`user.go-im.internal`；不转发用户Token。只把User的明确`PermissionDenied`视作撤权，异常回包、非正版本、证书/网络/超时等返回错误。配置全空时旧单聊和非团队群仍可用，团队群拒绝投递并保留Kafka重试；缺项则启动前报错。
3. Push针对团队群逐接收人先取当前IM名单，再查User活动版本、IM当前群资格和关闭记录，紧贴在线HTTP发送或离线INSERT。明确拒绝跳过该人；错误聚合返回给原Kafka消费者，不提前提交。在线发送失败后要保存离线时重新执行两项核权，不能用发送前的旧结果。旧非团队群与单聊仍走原路径，机器人ID碰撞的现有跳过规则保持。

定向仓储、Push路由与客户端测试覆盖无关闭行、关闭代际、撤权、坏行/SQL故障、在线/离线发送、在线失败后资格变化、缺客户端、错范围回包和不转发Token。`go test ./internal/repository -run TestCheckTeamGroupMemberGeneration -count=1 -timeout=90s`、`go test ./internal/push ./cmd/push -count=1 -timeout=90s`和全仓`go test ./... -count=1 -timeout=120s`结果见本轮汇报。

限制：基础Compose尚无Push/User双向TLS证书和地址，031—033/032真实迁移、MySQL锁与容器/云端链路未验收。跨User撤权、IM核验和WS写出没有分布式原子性；已入队消息不能撤回，不能承诺瞬时零窗口。部分成员成功后重试原Kafka消息仍可能重复在线发送，沿既定稳定`msg_id`由客户端去重。旧WS内部聊天入口仍未补独立服务鉴别。公开本人退出与重入入口继续后续步骤；阶段7/A16仍不标完成。

实际修改：[群仓储](../internal/repository/group_repo.go)、[仓储测试](../internal/repository/group_repo_test.go)、[Push资格客户端](../internal/push/team_eligibility_client.go)、[客户端测试](../internal/push/team_eligibility_client_test.go)、[投递路由](../internal/push/pusher.go)、[原路由测试适配](../internal/push/pusher_test.go)、[团队投递测试](../internal/push/team_delivery_test.go)、[Push启动](../cmd/push/main.go)、[本记录](stage7-push-team-delivery-guard-contract.md)、[决策](architecture-decisions.md)、[进度](project-plan.md)、[部署说明](../deploy/README.md)。
