# 后台触发资格核对契约（2026-10-04）

沿用户已确认A60专用mTLS受限RPC，不新增权限或数据归属方案。本批只建 IM→User 资格核对通道，未来IM从自身已保存trigger取原actor/team后调用；Agent不能直连此User服务。IM保存来源，User拥有当前用户/团队成员；备选让IM直接读User表越过边界、让Agent任意actor查User、沿用本人Token/伪造委托metadata均不采用。此细节是A60的具体受限调用分解，不授权通用模拟登录。未实现Agent→IM来源接口或模型/队列。本轮最多九步。

## 共同协议和授权边界

新独立user.UserTrigger服务仅一个CheckTriggerTeamMember(actor_id,team_id)，响应只echo两个已核对的ID，不含角色/资料/密码/Token。单独trigger.proto与generated，不改原user.proto/User接口、字段或认证。rpc/user/trigger_contract.go的triggerTeamServer已定义、默认Unimplemented，主agent共同文件，不由执行agent修改。

内置当前源UserTrigger实例有db与imDNSName。方法首先RequireServiceIdentity(ctx,imDNSName)：TLS1.3、已验证叶证书链、exact DNS SAN，无CN/wildcard/metadata替代；再ctx/正ID/db，3秒总ctx。只读team_members JOIN users SELECT users.status，WHERE team_members.team_id和user_id来自请求。确为成员且status=1才echo；缺成员/禁用同PermissionDenied，坏状态不允许，DB故障安全Unavailable，ctx取消/超时对应codes；不依赖JWT或普通GetMyInfo，不返回任何用户其他列。User依可信IM传来的已保存范围；IM来源查验和当前群资格下批实现，不能把此资格echo当全范围授权。

rpcauth.NewServiceClientCredentials(files,serverDNSName)、NewServiceServerCredentials(files,clientDNSName)、RequireServiceIdentity(ctx,name)由root新增通用角色化helper，仅本新通道使用；原Agent/IM bot credentials/handler不改。证书CA验签加服务SAN双重约束，信任同CA不等于允许全部服务。Server认证只有IM客户端；client验证User服务器；Handler还防误注册plain端口。

## 三个执行模块

1. 资格逻辑：仅新增 rpc/user/trigger_team.go/test.go。在共同triggerTeamServer上实现固定pb方法；使用真实GORM/sqlmock测试正成员、离队/禁用、数据库/取消、坏ID、plaintext/其他服务/伪metadata无SQL、无Token成功及echo范围。SQL行/返回数据异常拒绝。不改userServer或普通方法。

2. User专用监听与启动：仅 rpc/user/main.go、新trigger_listener.go/test.go。固定userTriggerConfig{ListenOn,IMDNSName string;Files rpcauth.CertificateFiles}；loadUserTriggerConfig(getenv func(string)string)(userTriggerConfig,error)读取USER_TRIGGER_LISTEN_ON、USER_TRIGGER_IM_DNS_NAME、USER_TRIGGER_TLS_CERT_FILE/KEY_FILE/CA_FILE。全部空返回禁用；部分/空白/无效listen地址或DNS(无空白、wildcard)拒绝（外部配置端口1—65535，newRuntime测试127.0.0.1:0可以）。newUserTriggerRuntime(c userTriggerConfig,users *userServer)(*userTriggerRuntime,error)：禁用nil；启用必须db已准备，严格mTLS，独立grpc.Server MaxRecvMsgSize4096只注册UserTrigger，禁止普通User/reflection。runtime字段server/listener供root真实TCP测试，Stop关两者，幂等。main加载早期检查完整环境、-profile要求/端口不与普通ListenOn冲突，再初始化db与zrpc；独立listener goroutine Serve故障结束主server，Stop由主进程拥有。普通端口仍只pb.RegisterUserServer。旧JWT/main/profile行为不变。共享root triggerTestCertificates(t)可供有效TLS配置测试，临时证书不提交。

3. IM受限客户端：仅新增 rpc/im/trigger_team_client.go/test.go。triggerTeamClientConfig{Addr,ServerDNSName string;Files rpcauth.CertificateFiles}，loadTriggerTeamClientConfig(getenv)(...,error)读取IM_TRIGGER_USER_RPC_ADDR、IM_TRIGGER_USER_TLS_SERVER_NAME、IM_TRIGGER_USER_TLS_CERT_FILE/KEY_FILE/CA_FILE；全部空禁用，部分配置拒绝，不接受URI/plaintext fallback或空白地址，端口1—65535。newTriggerTeamClient(c)(*triggerTeamClient,error)禁用nil；严格NewServiceClientCredentials+grpc.NewClient，最大接收4096、不自动重试，client内部pb.UserTriggerClient +conn。Check(ctx context.Context,actorID,teamID int64) error：正ID、本地ctx和配置守卫；2秒总ctx，metadata.NewOutgoingContext(ctx,metadata.MD{})不转发authorization/actor/service或其他用户metadata；仅pb请求2ID，响应必须非nil并完全echo所请求2ID，否则Unavailable。服务返回PermissionDenied/Unauthenticated/Canceled/DeadlineExceeded保留，其余错误安全Unavailable，无原DB错误/证书内容；Close关conn，禁用可nil安全。本批仅可用客户端与替身/实际TLS连接测试，不改IM main或暴露新的HTTP。

## 分工与验证

共同1＋资格2＋监听2＋客户端2＋root实际TLS/SQL组合1＋集中审查1＝九步。从同共同提交复用三worktree，各两个或三个允许文件；子agent仅编辑/gofmt/diffcheck，不测试/build/审批或commit/merge/push。不改协议/generated/TLS共用helper/依赖/schema/docs。root定向、全量Go和Linux User/IM编译；真实TCP mTLS配临时证书+SQL替身验证允许IM、拒同CA Agent、错误服务器名/CA/plaintext、User当前资格变化、普通端口不暴露新方法。没有生产证书、真实SQL/User/IM运行、云/容器/模型；原Outbox开关保持false。
