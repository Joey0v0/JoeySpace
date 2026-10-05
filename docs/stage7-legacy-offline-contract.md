# 阶段7：旧离线入口转发IM契约

2026-10-05，基线c6394e8，整合分支codex/stage7-legacy-offline-bridge。沿用户已选A22统一Gateway与A74当前资格过滤，只收口旧Gin离线读/ACK出口，不启动A73未读表或扩大群管理功能。共同准备提交尚不代表转发生效，必须整合三个任务并验证。

## 1. 兼容与调用链

- 旧GET `/api/v1/message/offline`及POST `/api/v1/message/offline/ack`路径保持。旧Gin认证中间件仍在；转发完整原Bearer，由IM重新验签/派生本人，不把HTTP user_id作为RPC授权，不转发其他请求头。
- 旧HTTP200＋业务code约定保持，成功GET为数组、空列表为`[]`；ID/from_id/to_id/initiator_id保持字符串，旧sender_type=0归用户1，机器人2及卡片内容保持。InvalidArgument→ErrBadRequest、Unauthenticated→ErrUnAuth、PermissionDenied→ErrForbidden，其余故障→ErrInternal；不输出原始RPC错误或部分正文。
- List/Ack使用共享OfflineMessagesClient（接口只两方法），不得调用旧MessageService离线读/删作为兜底。nil客户端、RPC失败或nil响应均拒绝，不读本地数据库。历史接口本批保留原实现。
- POST保留明确字符串ID列表、1—1000项及正数校验；只将这些ID传IM，重复ACK由IM已有本人条件保证。增加与Gateway相同的32KiB及单个JSON限制，旧ShouldBindJSON曾接受的超长/尾随JSON现在拒绝；正常请求保持兼容。ACK不是会话已读，本批不加资格判断或自动ACK。
- 读取先完成整个RPC，再生成响应；nil消息、非法时间/身份/聊天类型不得转换成正常数据。转发ctx由HTTP请求派生，设3秒总上限，较短上游deadline/取消继续生效；只转一个规范Bearer，不转自报身份、idempotency或服务凭证。
- cmd/api复用一个grpc.ClientConn，默认`localhost:9002`，可由既有`IM_RPC_ADDR`覆盖；容器为`im-rpc:9002`。客户端初始化失败停止启动，不降级本地查询；惰性连接建立不等于IM就绪。进程返回时Close，不扩本批为旧API完整优雅停机。
- 不新增语言/依赖/协议/迁移/中间件/数据归属；普通gRPC沿现有业务RPC，不混入专用mTLS内部入口。未修A16离队清理或Push投递资格，跨服务权限核对不承诺与撤权原子一致。

## 2. 八步批次与并行边界

1. root读进度、审查旧出口，确定既有A22/A74内兼容范围。
2. root固定接口/构造器/路由签名及本契约，保存共同起点；cmd/api暂传nil，仅为共同代码可编译，不标实现完成。
3. A完成旧GET/ACK RPC转发与既有Handler测试适配。
4. B完成cmd/api客户端地址/资源接线及本机配置测试。
5. C完成旧Gin HTTP→实际TCP gRPC→生产IM处理器组合测试。
6. root审查各文件、执行定向测试/保存提交，按A/B/C整合；只修实际发现的问题。
7. root补Compose IM地址，运行相关与全仓Go验证。
8. root更新架构/进度/协作/验收/部署和实际文件清单，交付审查；不合main/push/部署。

三个执行目录从同一共同提交建立，均禁止改共同接口、router、文档、Compose、依赖/协议/迁移，禁止Git写入/test/build/自行合main或部署。执行agent可编辑、gofmt和diffcheck；root集中测试与提交。

| 角色 | 绝对目录与分支 | 唯一允许修改 |
| --- | --- | --- |
| A：Handler | D:/zy/GoLang/go-im/.worktrees/assignee-backend；codex/stage7-legacy-offline-handler | internal/handler/message_handler.go、internal/handler/message_handler_test.go；可新增internal/handler/offline_rpc.go |
| B：启动 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway；codex/stage7-legacy-offline-startup | cmd/api/main.go；可新增cmd/api/offline_rpc.go、cmd/api/offline_rpc_test.go |
| C：组合 | D:/zy/GoLang/go-im/.worktrees/assignee-ui；codex/stage7-legacy-offline-flow | 仅新增rpc/im/legacy_offline_flow_test.go |

组合C读取共同接口、构造器和路由，测试当前资格过滤/故障无部分返回、原Token字符串ID/机器人、本人明确ACK及无效输入；业务Handler实现待A整合后由root运行，不自行改其他文件。SQL/User仍替身，本机HTTP/gRPC是真实连接，不称真实MySQL/容器/云验收。

## 本批实现与审查

共同起点a953968；A Handler f634c31、B启动a1df2a1、C组合da7f9b6均由root审查/测试后保存，无冲突合入codex/stage7-legacy-offline-bridge。C运行前由root快进A到其worktree，确保测试实际生产Handler；子agent未自行提交、测试/build、合main或部署。三个worktree保留，main仍89e2a1e。

八步实际结果对应上方安排：共同接口/构造器/路由；Handler转发及请求/响应检查；共享客户端及必要退出资源释放；四项HTTP/TCP RPC组合；root定向验证和整合；Compose地址/启动依赖；集中回归；本页与进度/决策/协作/验收/部署记录。未夹带A73未读存储、页面、A16清理、协议或依赖变更。

调用链：旧客户端GET/ACK → Gin原JWT中间件 → Handler原Bearer＋3秒请求ctx → 同一IM客户端 → IM本人读取/明确ACK；群读取再经IM当前群资格及User团队核权。没有本地读取/删除兜底。GET收到整页合法回包才输出，核权故障无部分正文；旧HTTP200业务码、字符串ID、sender_type=0→用户1、机器人/卡片和空数组保持。ACK的新输入限制见第1节。

客户端构造为惰性：地址校验先于基础设施，默认/容器及IPv6目标保持；runAPI在初始化/监听错误时返回，执行连接Close，不将此结构变更称为完整HTTP信号停机验收。IPv6 zone的百分号按当前gRPC目标解析规则编码，工厂测试包含此类地址；没有为测试连接外部服务。

全部实际修改文件（16个）：

| 文件 | 改动 |
| --- | --- |
| [IM客户端接口](../internal/handler/offline_client.go) | 仅离线读/明确ACK两方法，固定共同依赖 |
| [旧路由](../internal/handler/router.go) | 注入IM客户端 |
| [消息Handler](../internal/handler/message_handler.go) | 离线GET/ACK转发，历史保留 |
| [转发辅助](../internal/handler/offline_rpc.go) | 原Token/总超时、固定错误码、整页校验 |
| [Handler测试](../internal/handler/message_handler_test.go) | 无回落、取消/时限、身份和输入/响应边界 |
| [API启动](../cmd/api/main.go) | 创建共享客户端，路由接线，错误返回释放连接 |
| [客户端工厂](../cmd/api/offline_rpc.go) | IM_RPC_ADDR、本机缺省、host:port及惰性连接 |
| [工厂测试](../cmd/api/offline_rpc_test.go) | 地址/覆盖/Idle/Close与异常环境 |
| [HTTP→IM组合](../rpc/im/legacy_offline_flow_test.go) | 实际Gin路由/认证和生产IM TCP RPC |
| [Compose](../deploy/docker-compose.yaml) | 旧API IM地址及service_started依赖 |
| [部署说明](../deploy/README.md) | 旧API/IM配套升级、失败行为与配置 |
| [本契约](stage7-legacy-offline-contract.md) | 接口、八步、精确允许文件及审查 |
| [架构决策](architecture-decisions.md) | 既定A22/A74内迁移取舍和兼容代价 |
| [项目计划](project-plan.md) | 当前成果与下一批未读共同契约 |
| [协作记录](worktree-collaboration-plan.md) | 三worktree分工/交付与root整合 |
| [阶段7验收](stage7-acceptance.md) | 旧出口保护和仍未验收范围 |

验证记录：root在A执行`go test ./internal/handler -count=1`通过；B执行`go test ./cmd/api -count=1`通过，最终IPv6工厂补项后重测也通过；C吸收A后执行`go test ./rpc/im -run '^TestLegacyOfflineHTTP' -count=1`四项通过；整合后`go test ./... -count=1`通过。PyYAML解析实际基础Compose并核对IM地址和依赖通过，不等于Docker Compose运行验收。

测试中的HTTP/Gin JWT认证、本机TCP gRPC和IM处理器真实执行，SQL/User仍替身，不能称真实MySQL/云端验收。没有真实故障进程/完整旧API关闭验证；当前机器无Docker，不验证容器启动、覆盖组合或挂载；模型/浏览器及迁移仍留最终统一验收。页面不变，不重复上一批303项Node。没有删除生成文件；未合main/push/部署，A73未读与A16退出清理等仍待后续小步。
