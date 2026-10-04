# IM→User 受限资格核对审查（2026-10-04）

业务代码1db0499，分支codex/trigger-team-auth-integration。用户继续后，上批6ac032f快进合本地main；本批共同2c00b9c，root保存资格c1fd2a4、监听b524afa、客户端0c21371并无冲突整合，补精确服务器身份与真实TLS组合。main仍6ac032f，三个原worktree干净保留，未push或云同步。

沿用户已确认A60，完成后台安全读取的资格通道。User只接受证书指定的IM服务，只查当前团队成员与有效账户，不发登录Token、不返回资料/角色。IM未来从其已保存触发派生actor/team，Agent不能直连本通道；尚未实现Agent→IM来源读取，也未接IM进程客户端。User资格成功不能当完整群权限或消息授权。

## 九步实际交付

| 步骤 | 实际目标与改动 | 验证 |
| --- | --- | --- |
| 1 共同 | 新独立UserTrigger协议/generated、角色化TLS校验、独立handler声明、临时证书fixture及共同契约 | 共同User/IM与rpcauth回归通过，旧User协议未改 |
| 2 当前资格 | User先验证IM服务身份，再查team_members JOIN users，只读status，3秒限制 | 无Token/大ID完整echo、当前成员有效性通过 |
| 3 拒绝路径 | 最多读两行，拒缺成员/禁用、NULL/重复或坏数据，错误脱敏及取消 | 伪metadata/错误TLS、SQL/状态/取消测试通过 |
| 4 专用监听 | 配置全空关闭、部分拒绝；启用须-profile/DB/独立端口，TLS13只允许IM SAN，MaxRecv4096 | 端口/配置/数据库与证书守卫通过 |
| 5 接入生命周期 | 普通端口仍仅User；注册回调后启动专用Serve；专用失败停止实际普通grpc.Server，幂等Stop | 实际服务隔离、4KiB、故障停止、框架等待模拟及panic清理通过 |
| 6 IM客户端 | 专用双向TLS与独立UserTrigger、禁用RPC自动重试，2秒总超时/4096上限 | 实际TCP TLS、正确范围与拒绝路径通过 |
| 7 客户端边界 | 清空caller outgoing metadata，严格双ID echo、错误泛化；连接关闭Unavailable，caller取消优先 | Token隔离、伪响应、晚成功、关闭/期限与无额外RPC通过 |
| 8 root组合 | 实际生产监听/handler＋SQL替身，合法IM/同CA Agent/错服务器名/plain，普通端口无方法/误注册拒绝 | 当前资格变化与消息认证边界通过；新增wildcard服务器SAN拒绝通过 |
| 9 审查 | 集中保存/整合、最终全量Go与Linux User/IM构建，完整文档/文件清单 | 全部通过，最后文档不追加业务目标 |

## 调用链与选型理由

未来IM从其持久触发取得actor/team→本批triggerTeamClient.Check→清空传出metadata、2秒RPC→User专用TLS端口→CA验证＋exact IM SAN＋handler再次RequireServiceIdentity→User自有team_members/users当前状态→仅echo已核对的两个ID→IM客户端严格比较。每次重新核对，不缓存资格；离队/禁用拒绝。响应不含用户名、密码、成员角色，也不取得登录身份。

数据仍分别归User/IM。备选IM跨库读User、Agent按任意actor直连User、借存库JWT或伪metadata都未采用。专用协议/端口不给旧User服务增加无Token分支；同CA的Agent身份也拒绝。新TLS helper同时要求客户端和服务器精确SAN，wildcard/CN不替代服务身份；原机器人mTLS函数不变。这是用户A60的受限调用分解，非新框架/中间件或通用用户模拟权限。

User新监听环境变量：USER_TRIGGER_LISTEN_ON、USER_TRIGGER_IM_DNS_NAME、USER_TRIGGER_TLS_CERT_FILE、USER_TRIGGER_TLS_KEY_FILE、USER_TRIGGER_TLS_CA_FILE，全空关闭。启用仍需原-profile/数据库配置、不同于普通User的端口。

IM新客户端环境变量：IM_TRIGGER_USER_RPC_ADDR、IM_TRIGGER_USER_TLS_SERVER_NAME、IM_TRIGGER_USER_TLS_CERT_FILE、IM_TRIGGER_USER_TLS_KEY_FILE、IM_TRIGGER_USER_TLS_CA_FILE，全空关闭；本批构造/查询可用，但IM main尚未装配。需要真实IM客户端身份与User服务器证书，临时测试证书只写临时目录。

## 实际检查与限制

资格包、监听包、客户端新功能及整合TLS定向集中执行。首次客户端实际TLS测试发现关闭连接后gRPC Canceled被当成调用者取消；已修生产判断为Shutdown→Unavailable，caller取消仍优先，补调用中关闭后定向及最终全量通过。root在审查中补了监听故障停止实际普通gRPC的行为测试，并收紧精确服务器SAN；未把初次失败当通过。

最终 `go test ./... -count=1` 全量通过，新增30个Go测试函数（含参数化子项）。Linux amd64 User与IM交叉编译通过，产物仅临时目录。root只用已有protoc31.1/plugins生成新trigger协议，没有删除旧generated、升级依赖或执行迁移。未改前端，不重复上批已通过187项Node/未变服务构建；规范Go换行后Git只留已验证的实际语义修改。

真实本机TCP/TLS、临时证书与实际生产listener/client各自组合已经验证，数据库仍sqlmock，IM客户端实际TLS测试的User为RPC替身；没有将两个main包接进真实进程/数据库。Linux go-zero Start的shutdown等待用受控行为模拟核对，未在Linux运行信号/异常进程测试；编译通过不等于生产生命周期验收。

未连接真实MySQL/云/容器、配置生产证书/CA、执行022或模型调用。Outbox默认开关仍false，群内@AI生成尚未完成。下一批实现Agent→IM受限来源：仅按已保存触发访问、核对当前群资格，接本批User客户端，再进入持久队列/租约最多两次生成。Task创建/群回帖仍本人确认，不能扩大既有A41/A46。

资格约4分9秒、监听初稿10分19秒＋生命周期补齐1分42秒、客户端初稿约7分23秒并补关闭修复；这是并行交付记录，没有单agent对照，不声称固定倍数提速。

## 全部20个实际修改文件（相对main 6ac032f）

| 文件 | 用途 |
| --- | --- |
| [trigger.proto](D:/zy/GoLang/go-im/rpc/user/trigger.proto) | 独立专用资格协议 |
| [trigger.pb.go](D:/zy/GoLang/go-im/rpc/user/pb/trigger.pb.go) | 新消息生成代码 |
| [trigger_grpc.pb.go](D:/zy/GoLang/go-im/rpc/user/pb/trigger_grpc.pb.go) | 新服务生成代码 |
| [trigger_contract.go](D:/zy/GoLang/go-im/rpc/user/trigger_contract.go) | 独立handler声明 |
| [trigger_team.go](D:/zy/GoLang/go-im/rpc/user/trigger_team.go) | 当前资格核对 |
| [trigger_team_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_team_test.go) | 身份/SQL/状态拒绝 |
| [trigger_listener.go](D:/zy/GoLang/go-im/rpc/user/trigger_listener.go) | 专用TLS监听和有限等待helper |
| [trigger_listener_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_listener_test.go) | 配置/TLS/生命周期 |
| [main.go](D:/zy/GoLang/go-im/rpc/user/main.go) | 可选监听接线 |
| [trigger_tls_fixture_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_tls_fixture_test.go) | 临时证书fixture |
| [trigger_tls_flow_test.go](D:/zy/GoLang/go-im/rpc/user/trigger_tls_flow_test.go) | root真实TLS/SQL组合 |
| [trigger_team_client.go](D:/zy/GoLang/go-im/rpc/im/trigger_team_client.go) | IM专用客户端 |
| [trigger_team_client_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_team_client_test.go) | metadata/echo/关闭/TLS |
| [service.go](D:/zy/GoLang/go-im/internal/rpcauth/service.go) | exact服务双向TLS |
| [service_test.go](D:/zy/GoLang/go-im/internal/rpcauth/service_test.go) | 证书身份与wildcard拒绝 |
| [trigger-team-auth-contract.md](D:/zy/GoLang/go-im/docs/trigger-team-auth-contract.md) | 固定契约及协作范围 |
| [architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A60实施/备选/代价 |
| [project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 当前成果及下步 |
| [worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 三角色工作树与交付 |
| [trigger-team-auth-review.md](D:/zy/GoLang/go-im/docs/trigger-team-auth-review.md) | 本审查记录 |
