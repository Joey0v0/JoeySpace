# 阶段7：IM团队群退出清理受限入口

2026-10-06，沿已确认的A75方案，在`codex/stage7-team-leave-intent`准备IM专用清理协议与处理器。本批只解决“可信User服务怎样请求IM关闭固定资格代际”；不开放生产监听、不让普通IM/Agent/浏览器调用。

## 本批实现与审查

1. root新增`rpc/im/leave.proto`及由仓库已有protoc 31.1、Go插件生成的`rpc/im/pb/leave.pb.go`和`rpc/im/pb/leave_grpc.pb.go`。独立`IMLeave.CloseTeamGroupMemberships`仅接受正team/user/generation，回显持久关闭到的最高代际；不带Token、用户名或消息正文。
2. root新增`rpc/im/leave.go`：先以`rpcauth.RequireServiceIdentity`核对精确User服务DNS SAN，再复用现有`closeTeamGroupMemberships`。后者在IM本地短事务中锁关闭记录、推进版本并删除该团队该用户的群成员；同代际或旧代际重试不重复删除，原消息、离线投递和阅读凭据不受本步更改。
3. root新增`rpc/im/leave_test.go`，使用临时测试CA和实际gRPC/TLS连接验证User证书可关闭、同请求重放回显、非法代际无写入、其他服务证书及明文拒绝；无TLS的处理器直调也先拒绝。旧关闭函数自身的回滚和坏数据测试继续保留。只读审查Agent核对了专用监听、固定元组、重放和错误边界，未修改文件。

关键调用链目前仅在测试中：User服务证书 → 独立测试mTLS连接 → IMLeave处理器 → IM关闭短事务。**生产`rpc/im/main.go`尚未注册IMLeave，也没有监听地址/证书配置或User调用客户端**，所以部署后尚不能发起清理。下一步须独立接入只信任User证书的监听器，并让User仅从033持久操作读固定team/user/generation后调用；不能把客户端自报三元组直接转发给IM。随后还需Push当前资格核对及User完成状态/重试，才能开放对外退出。

本地`go test ./rpc/im -run 'TestIMLeave' -count=1 -timeout=90s`通过；全仓Go结果见本批最终汇报。SQL为替身，本机证书为临时测试证书；031—033迁移、真实MySQL并发/崩溃及云端证书配置均未验收，不标A16或阶段7完成。

全部实际修改文件：`rpc/im/leave.proto`、`rpc/im/pb/leave.pb.go`、`rpc/im/pb/leave_grpc.pb.go`、`rpc/im/leave.go`、`rpc/im/leave_test.go`、`docs/stage7-team-leave-im-handler-contract.md`、`docs/project-plan.md`、`docs/architecture-decisions.md`、`docs/stage7-acceptance.md`。
