# 阶段7：IM退出清理专用监听

2026-10-06，沿A75和[上一批处理器](stage7-team-leave-im-handler-contract.md)，将IMLeave接入IM进程的可选独立mTLS监听。目标是让可信User服务将来能调用受限清理能力，同时保证普通IM、Agent Trigger、机器人证书与端口不能获得该能力。

## 本批实现与审查

1. `rpc/im/leave_listener.go`新增默认关闭的`IM_LEAVE_*`配置和运行时。四项环境变量必须全部提供：监听地址、IM服务端证书、私钥、信任CA；缺项、空白/非法地址或证书错误在监听前拒绝。客户端身份固定为`user.go-im.internal`的精确DNS SAN，不提供可把它改成Agent身份的环境项。监听只注册IMLeave，限制4KiB请求，Stop可重复调用。
2. `rpc/im/main.go`在准备数据库后绑定专用监听，核对与普通IM、Bot、Agent Trigger的端口冲突；服务启动、异常退出和资源释放沿现有专用监听协调路径。默认未配置时不创建新端口或证书依赖。仍不新增User调用客户端，也不把清理方法注册到普通IM入口。
3. `rpc/im/leave_listener_test.go`验证配置缺项/端口冲突、数据库/证书/占用端口拒绝、独立服务注册、真实临时CA下User证书成功和Agent证书拒绝。定向`go test ./rpc/im -run 'TestIMLeave' -count=1 -timeout=90s`及全仓`go test ./... -count=1 -timeout=90s`结果见最终汇报。只读审查Agent核对了Trigger/Bot/Leave组合的资源生命周期，未修改文件。

运行边界：现有Compose没有注入`IM_LEAVE_*`或挂载专用证书，默认仍关闭；即使手动开启，User尚未从033固定操作发起调用，Push资格和本人退出入口也未接线。测试使用本机临时证书与SQL替身，没有验证三种专用监听同时开启的真实进程、实际Compose挂载、真实MySQL锁/故障或云端证书。开启前需先执行032，User退出业务启用前还需031/033及后续链路，不能把监听就绪等同于可用的退出功能。

本批实际修改文件：`rpc/im/leave_listener.go`、`rpc/im/leave_listener_test.go`、`rpc/im/main.go`、`rpc/im/README.md`、`deploy/README.md`、`docs/stage7-team-leave-im-runtime-contract.md`、`docs/project-plan.md`、`docs/architecture-decisions.md`、`docs/stage7-acceptance.md`。
