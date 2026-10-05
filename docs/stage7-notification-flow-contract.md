# 阶段7：任务通知组合与恢复验证

2026-10-05；基线3de7a1a，main89e2a1e，整合分支codex/stage7-notification-flow-verification。沿A68—A72，不新增接口、权限、迁移、依赖或后台能力。六步：契约、发布到消费组合、TLS到实际WS帧、查询/已读恢复组合、集中验证、验收与进度记录。测试只补相邻组件协作缺口，不重复已有参数矩阵；不能把分段组合说成真实MySQL/Kafka/浏览器全链路验收。

root拥有共同文档、审查、测试和Git写入。三个执行agent各在独立worktree编辑一份测试文件；禁止测试/build、Git写入、合main/push/部署及修改生产文件或共同文件。发现生产缺陷或必须改变既定语义，先报告root，不自行扩大范围。

## A：Task发布器到Push消费

唯一目录D:/zy/GoLang/go-im/.worktrees/assignee-backend，分支codex/stage7-notification-publication-flow。唯一允许新增rpc/task/notification_delivery_flow_test.go。

复用当前Task测试SQL/发布器替身，生产Task状态变更和生产发布器固定事件后，将实际输出的Key/Value交给生产Push TaskNotificationConsumer。Kafka writer/reader、在线路由/发送结果为替身，reader仅补真实broker会提供的Topic/Partition/Offset，不重编码Key/Value。覆盖发布ACK或MarkPublished结果不确定，重建发布器复用原内容/Key；消费者处理重复事件只确认对应offset，不改变本人通知/已读，不声称恰好一次。可以复用现有publisher/status helper，不在本文件引入大型通用测试框架。所有阻塞操作有超时，Run取消并等待，writer/reader按所属对象释放。

## B：专用TLS处理器到实际WS帧

唯一目录D:/zy/GoLang/go-im/.worktrees/assignee-gateway，分支codex/stage7-notification-websocket-flow。唯一允许新增internal/ws/task_notification_socket_flow_test.go，包ws。

复用本包notificationTLSFiles临时证书，真实mTLS HTTP调用生产NewTaskNotificationHandler；创建本机真实gorilla WebSocket两端，以生产Client.writePump传送最小提醒并由对端ReadMessage核对精确type/version/字符串大ID及无Token/接收人/详情。直接在测试中受锁配置Hub，不启动没有停止入口的Hub.Run；不调用Client.Start/readPump以免要求真实Redis/聊天消费。临时User故障→503且不入队→同事件手动重试成功、明确离队/Token失效→denied且不入队；用队列/调用记录确认失败未投递，不能把ReadMessage超时后继续复用socket。HTTP/WS有超时，writePump关闭并等待，无sleep式猜测。不能在ws包导入push造成循环；此段HTTPS客户端由生产rpcauth配置与标准http.Client组成，不宣称测试了生产Push客户端（已有Push真实TLS测试保留）。

## C：生产Task RPC查询与已读恢复

唯一目录D:/zy/GoLang/go-im/.worktrees/assignee-ui，分支codex/stage7-notification-read-recovery-flow。唯一允许新增rpc/task/notification_read_flow_test.go。

复用SQL/User helpers，将生产taskServer注册实际本机TCP gRPC。组合List→Mark读写→响应在成功提交后被测试拦截器转换为Unavailable→本人再次List观察原已读时间→再次Mark仅锁读且不重复UPDATE。身份只来自原Token，覆盖资格撤销后两接口均拒绝且不触发SQL，恢复资格仍能读取原记录；SQL/User仍替身，不能称真实数据库持久恢复。无需重写已有参数校验或增加HTTP/Gateway假业务服务器。拦截器故障仅针对首次成功Mark，不改变生产授权/提交语义；SQL expectations证明更新时间固定且拒绝时无额外查询。所有RPC有超时，服务与连接关闭。

## root集中验证与交付

先分别在对应worktree运行新增测试及所在包，检查允许文件和阻塞资源后保存提交；再无冲突整合三分支，运行全仓Go回归。生产页面本批不变，不重复刚通过的303项Node，保留此前页面验证范围。必要时只修新测试；生产问题另行说明和修复，关键选择先讨论。

新增阶段7验收清单：串起正常任务通知、离线查询、显式已读、重复事件/临时失败/坏事件、权限撤销、迁移/证书/Topic/group/开关与完整演示证据；逐项区分本地分段验证、真实环境待执行及未读/执行记录仍待审查。同步project-plan、architecture-decisions与worktree记录，不标阶段7或整体项目已完成，不合main/push/部署。
