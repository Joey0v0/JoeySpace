# 阶段7：旧离线入口转发IM契约

2026-10-05，基线c6394e8，整合分支codex/stage7-legacy-offline-bridge。沿用户已选A22统一Gateway与A74当前资格过滤，只收口旧Gin离线读/ACK出口，不启动A73未读表或扩大群管理功能。共同准备提交尚不代表转发生效，必须整合三个任务并验证。

## 1. 兼容与调用链

- 旧GET `/api/v1/message/offline`及POST `/api/v1/message/offline/ack`路径保持。旧Gin认证中间件仍在；转发完整原Bearer，由IM重新验签/派生本人，不把HTTP user_id作为RPC授权，不转发其他请求头。
- 旧HTTP200＋业务code约定保持，成功GET为数组、空列表为`[]`；ID/from_id/to_id/initiator_id保持字符串，旧sender_type=0归用户1，机器人2及卡片内容保持。InvalidArgument→ErrBadRequest、Unauthenticated→ErrUnAuth、PermissionDenied→ErrForbidden，其余故障→ErrInternal；不输出原始RPC错误或部分正文。
- List/Ack使用共享OfflineMessagesClient（接口只两方法），不得调用旧MessageService离线读/删作为兜底。nil客户端、RPC失败或nil响应均拒绝，不读本地数据库。历史接口本批保留原实现。
- POST保留明确字符串ID列表、1—1000项及正数校验；只将这些ID传IM，重复ACK由IM已有本人条件保证。ACK不是会话已读，本批不加资格判断或自动ACK。
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

## 3. 审查记录

实际实现、全部文件和验证结果在整合后补充。main保持89e2a1e，既有worktree保留，不删除生成文件。
