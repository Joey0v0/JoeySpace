# Agent→IM 持久触发上下文：本轮审查

日期：2026-10-04。沿已确认A60，接通后台只读来源范围；不是后台草稿生成完成。本轮从本地main6592d28建立codex/trigger-context-integration，共同7737896；root调度三个保留worktree，不删除旧文件/目录，不push或云同步。

## 九个独立步骤

| 步骤 | 做了什么及目的 | 调用关系/审查重点 |
| --- | --- | --- |
| 1 | root统一独立IMTrigger协议、声明与共同契约 | 请求仅message_id，全部actor/team/group由IM保存记录派生；新生成文件，不修改旧生成文件 |
| 2 | 后端校验保存Outbox和原群消息 | ID/msg_id/发送者/群/指令/服务器时间一致；NULL、重复、无效或篡改记录拒绝 |
| 3 | 后端检查当前权限后读取固定范围 | 当前IM群→User当前团队/有效账户→历史SQL再限定当前群，最多20条且ID≤来源；未授权不返回正文 |
| 4 | 监听接IM进程及上批User资格客户端 | 新独立mTLS精确Agent身份，只注册只读服务；部分配置或端口冲突拒绝，无明文/JWT兜底 |
| 5 | 验证配置、端口隔离及退出行为 | 真实grpc服务停止，新增监听故障不等待Linux框架额外shutdown通知；停止不拥有DB |
| 6 | Agent提供后台只读客户端 | 请求只来源ID、清所有metadata、5秒预算、禁用RPC重试、2MiB回包上限 |
| 7 | Agent校验成功响应与证书边界 | 来源/指令/时间/key/历史顺序及sender身份严格一致，关闭连接/取消/无效回包拒绝 |
| 8 | root实际TCP/mTLS组合 | Agent生产client→IM生产listener/handler/GORM(SQL替身)→User生产client→User RPC替身；离队/退群/服务不可用不读历史 |
| 9 | root集中验证及方案记录 | 全文件、验证证据、限制和后续如实记录；不执行真实DB/模型/部署 |

## 关键调用链

后续worker接收仅含来源ID的通知 → Agent.TriggerContextClient.Read → 独立IMTrigger mTLS → IM保存Outbox和原消息比对 → 当前群资格 → IM专用mTLS客户端 → UserTrigger当前团队/有效账户 → IM带当前成员条件的固定上界历史 → Agent严格验证来源及返回范围。

published=false也允许核对，覆盖Kafka ACK已成功但Outbox标记尚未保存的窗口。日期参考原消息数据库保存时间。固定消息ID上界不等于完整上下文快照；跨服务资格检查不等于分布式事务，不声称检查后永远不再变化。每次请求重查当前资格，无授权缓存。普通Bearer IM、机器人回帖、Task确认权限不改变。

## 验证与尚未验证

业务整合提交0ef64fa，最终 `go test ./... -count=1` 全部通过；Linux amd64 的 `./rpc/im` 和 `./cmd/agent` 编译通过，产物只在临时目录，无部署。新增31个测试函数及参数场景（IM处理器8、监听10、Agent客户端10、root组合3）。没有修改页面/JS，不重复运行未变前端测试。

首次全量检查发现监听测试两处使用了不存在的旧UserId请求字段，IM测试未能编译；子agent仅删除两处测试字段，root保存1013aa6并整合后最终全量通过。业务Linux编译首次即通过，测试字段修复没有改生产代码，不重复构建。子agent仅gofmt/diffcheck，测试/build均root集中执行。

实际组合覆盖双mTLS传输、IM生产监听/处理器/客户端、Agent生产客户端及GORM+SQL替身，User服务在该组合为RPC替身（生产User处理器已在上批及本次全量验证），不等同真实User/MySQL多进程联调。大int64 ID未损失，published=false窗口可读取；离队/退群前后无正文、资格服务失败无历史读取、恢复资格重新读取而不缓存、同CA错误服务/意外明文注册均拒绝。客户端另测无效回包/2MiB限制/取消/关闭；监听故障测试使用真实普通RPC与模拟Linux框架额外等待，未运行真实Linux信号/进程演练。

仍未执行022迁移，Outbox开关仍false；未连接真实MySQL/Kafka/模型、浏览器、生产证书、Docker或腾讯云。Agent客户端本批未接main/worker，无持久排队、租约、两次自动生成或后台草稿页面；不能宣称群内@AI已经自动生成任务。旧负责人解析仍为用户Bearer入口，后续后台生成也须按A60补受限资格通道，不能伪造Token复用。Task创建/群回帖仍本人确认和显式重试。

## 全部实际修改文件（17个）

仅IM main是原有生产文件修改；其他为新增只读模块/协议/测试与共同文档。下列根目录链接为整合后审查位置。

| 文件 | 用途 |
| --- | --- |
| [trigger.proto](D:/zy/GoLang/go-im/rpc/im/trigger.proto) | 新独立只读协议 |
| [trigger.pb.go](D:/zy/GoLang/go-im/rpc/im/pb/trigger.pb.go) | 新协议消息生成 |
| [trigger_grpc.pb.go](D:/zy/GoLang/go-im/rpc/im/pb/trigger_grpc.pb.go) | 新服务生成 |
| [trigger_context_contract.go](D:/zy/GoLang/go-im/rpc/im/trigger_context_contract.go) | IM只读处理器与资格依赖声明 |
| [trigger_context.go](D:/zy/GoLang/go-im/rpc/im/trigger_context.go) | 持久来源/current资格/历史范围 |
| [trigger_context_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_context_test.go) | 来源/权限/异常数据/取消测试 |
| [main.go](D:/zy/GoLang/go-im/rpc/im/main.go) | 独立监听与User客户端接进程 |
| [trigger_listener.go](D:/zy/GoLang/go-im/rpc/im/trigger_listener.go) | 配置、TLS、监听及退出 |
| [trigger_listener_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_listener_test.go) | 隔离、依赖、端口、关闭验证 |
| [trigger_client.go](D:/zy/GoLang/go-im/rpc/agent/trigger_client.go) | Agent安全只读客户端 |
| [trigger_client_test.go](D:/zy/GoLang/go-im/rpc/agent/trigger_client_test.go) | 响应/metadata/TLS/期限边界 |
| [trigger_context_flow_test.go](D:/zy/GoLang/go-im/rpc/im/trigger_context_flow_test.go) | root实际TLS组合/资格撤销验证 |
| [trigger-context-contract.md](D:/zy/GoLang/go-im/docs/trigger-context-contract.md) | 共同接口及三Agent范围 |
| [trigger-context-review.md](D:/zy/GoLang/go-im/docs/trigger-context-review.md) | 本审查记录 |
| [architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A60所选方案/备选/理由/代价/确认状态 |
| [project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 当前成果和下一步 |
| [worktree-collaboration-plan.md](D:/zy/GoLang/go-im/docs/worktree-collaboration-plan.md) | 分工及交付/整合记录 |

交付：共同7737896、来源63f9668、监听5476476＋测试修复1013aa6、客户端7b263e5；root依监听→来源→客户端→修复无冲突整合，组合与本轮业务为0ef64fa。三个worktree干净保留，root整合分支供审查；main保持上轮6592d28，本轮未合main/未push。

三角色自行记录约8分46秒、8分1秒（另测试字段修复）、10分20秒。以实际并行交付为准，没有单agent对照或固定倍数效率结论。下一批按A61实现持久接收/执行状态及租约，模型/负责人解析/页面仍须按批准范围逐项完成。
