# 后台触发负责人解析：九步审查

2026-10-04。沿用户已确认A60专用mTLS受限读取与A48/A49精确姓名/本人审查。上批e6b561a已快进合本地main，本批codex/trigger-assignee-integration供审查；共同af09925、User2e81f56/测试修正d8b8089、IM67d0dbc、Agent580ce9f，三个worktree保留。没有push/删除旧文件/数据库迁移/模型调用/云同步。

## 九步实际范围

| 步骤 | 实际成果、解决的问题 | 边界 |
| --- | --- | --- |
| 1 | root扩现有UserTrigger/IMTrigger protobuf、生成代码、共同字面形状及能力接口 | 不给Agent输入任意actor/team/group，不改普通User/IM或机器人服务 |
| 2 | User先鉴别精确IM、当前启用作者资格，按团队查询完整用户名/昵称 | SQL二进制完整OR匹配、启用候选、作者当前资格EXISTS |
| 3 | User严格扫描全21行与前后资格核对、坏数据测试 | 最多20＋截断；撤权不能当未找到，不返回role/Token |
| 4 | IM重用保存来源读取/当前资格/有界历史，核对称呼原文，派生范围解析 | 没有持久来源或字面证据不能查询，不信任Kafka/metadata身份 |
| 5 | IM复用User专用conn，2秒/32KiB/清metadata，返回前再查当前群 | 原资格Check仍4KiB，不回退旧Bearer；当前资格不缓存 |
| 6 | Agent复用IM专用conn、5秒、只给sourceID/name及严格成功校验 | echo、正scope、稳定key、候选顺序/唯一/字段/总大小 |
| 7 | Agent基于已核实context验证称呼并转换5类负责人结果 | 只改负责人三个字段，错误零草稿；唯一匹配仍本人审查 |
| 8 | root实际本机TLS/SQL替身组合及共有形状测试 | User生产监听/handler单独验证；Agent→IM→User组合User是RPC替身 |
| 9 | root集中Go/Linux、旧监听白名单适配、完整审查与阶段记录 | 结果见下；只把实际验证的负责人通道标完成 |

## 调用链和业务含义

后台执行未来先取得租约→Agent受限读取已保存触发/当前资格→模型提取字面称呼→Agent校验原文→ResolveTaskTriggerMember(sourceID,name)→IM重新读取Outbox/原消息和有界历史、当前团队群资格→User ResolveTriggerTeamMember(savedActor,savedTeam,name)→User前后当前资格与候选SQL→IM再查当前群→Agent核对整个返回scope及原context一致→形成待本人审查候选。

本批实现链中负责人解析部分及草稿状态适配，未接worker/模型/完整生成保存。即使用户名/昵称唯一匹配也只预存候选ID，后续本人读取/选择/版本确认沿旧规则；无称呼none、未找到not_found、重名ambiguous、截断truncated保留区别，不悄悄指定。只改三个负责人字段，正文/来源/截止时间依据等原值保留。

两个新RPC仍只在既有独立TLS端口，复用原配置/证书/连接；普通JWT和机器人写服务不注册。服务证书不代替作者权限。User不跨读IM，IM不跨读users，Agent不模拟Token；返回候选只ID/用户名/昵称用于校验，无密码/role。姓名证据只指令或content_type=1文本，卡片/图片不是姓名依据。

成员与范围每次重查，但多个服务不是分布式授权事务，返回后资格仍可能变化；生成最终保存和Task确认还需相应当前资格检查。新解析单次32KiB，原User资格4KiB、IM来源2MiB不改变。

## 验证结果与限制

业务整合adb0cb1：最终 `go test ./... -count=1` 全部通过，Linux amd64 User、IM、Agent 编译通过，产物只在临时目录。新增41个测试函数：User8、IM handler7/client5、Agent14、共同形状2、root User TLS2、Agent→IM→User TLS3；大量参数场景另计。

首次User/形状集中检查：共同形状通过；旧User listener假定一个方法及新取消断言用空字符串判断泄露导致误报。root将User/IM原测试改精确两方法白名单、保留普通服务不可调用的隔离检查；子agent只修新取消断言，不改生产逻辑。修正由root保存d8b8089合入；整合后全量Go及三服务Linux编译通过。

User实际生产TLS监听＋真实handler/GORM配sqlmock验证当前资格、唯一/跨字段重名/空/21截断与查询过程中撤权；错误证书没有SQL。Agent→IM→User实际TLS组合使用生产Agent client、IM handler/既有来源读取、生产IM→User client；IM SQL为替身、User为有真实mTLS鉴别的RPC替身。不能称整个链都是真实User/MySQL进程；各段业务和SQL另有独立验证。

组合检查caller metadata清除、派生大ID/固定key、候选>4KiB可通过新32KiB单调用、错误echo/User失败/当前群撤销/缺字面证据/错误Agent证书拒绝。IM客户端实际TLS测试另验证原Check>4KiB仍拒、新Resolve>32KiB拒、旧User Unimplemented不回退；Agent实际TLS测试保留旧Read>32KiB可用。没有真实MySQL CAST/字符比较/并发撤权、生产证书、数据库/容器/真实模型或云验收，没有模型预算消耗/草稿持久保存/worker接线。

## 全部实际修改文件（28个）

| 文件 | 审查位置 |
| --- | --- |
| [rpc/user/trigger.proto](../rpc/user/trigger.proto) | 增加受限匹配方法/最小候选协议 |
| [rpc/user/pb/trigger.pb.go](../rpc/user/pb/trigger.pb.go) | 既有工具生成消息代码 |
| [rpc/user/pb/trigger_grpc.pb.go](../rpc/user/pb/trigger_grpc.pb.go) | 生成新方法，未删旧方法 |
| [rpc/im/trigger.proto](../rpc/im/trigger.proto) | 请求只sourceID/name及派生scope响应 |
| [rpc/im/pb/trigger.pb.go](../rpc/im/pb/trigger.pb.go) | 生成消息代码 |
| [rpc/im/pb/trigger_grpc.pb.go](../rpc/im/pb/trigger_grpc.pb.go) | 生成新受限方法 |
| [internal/model/agent_trigger_member.go](../internal/model/agent_trigger_member.go) | 名称/单候选形状、数量/响应常量 |
| [internal/model/agent_trigger_member_test.go](../internal/model/agent_trigger_member_test.go) | Unicode/长度/不归一化/候选字段 |
| [rpc/user/trigger_member.go](../rpc/user/trigger_member.go) | 精确IM鉴别、前后资格、严格SQL匹配 |
| [rpc/user/trigger_member_test.go](../rpc/user/trigger_member_test.go) | SQL/权限/NULL/截断/取消与错误 |
| [rpc/user/trigger_member_flow_test.go](../rpc/user/trigger_member_flow_test.go) | 生产UserTLS/handler/SQL替身组合 |
| [rpc/user/trigger_listener_test.go](../rpc/user/trigger_listener_test.go) | 精确新两方法白名单，原服务隔离保留 |
| [rpc/im/trigger_assignee_contract.go](../rpc/im/trigger_assignee_contract.go) | 新解析能力，保持旧Check接口 |
| [rpc/im/trigger_assignee.go](../rpc/im/trigger_assignee.go) | 保存范围/原文本证据及最终群资格 |
| [rpc/im/trigger_assignee_test.go](../rpc/im/trigger_assignee_test.go) | 源范围/幻觉/权限/非法响应 |
| [rpc/im/trigger_member_client.go](../rpc/im/trigger_member_client.go) | 既有conn/2秒/单调用32KiB与验证 |
| [rpc/im/trigger_member_client_test.go](../rpc/im/trigger_member_client_test.go) | echo、metadata、TLS/旧4KiB等 |
| [rpc/im/trigger_assignee_flow_test.go](../rpc/im/trigger_assignee_flow_test.go) | 实际Agent→IM→User证书链组合 |
| [rpc/im/trigger_listener_test.go](../rpc/im/trigger_listener_test.go) | 两方法白名单与旧服务隔离 |
| [rpc/im/trigger_team_client_test.go](../rpc/im/trigger_team_client_test.go) | 旧资格client替身声明新未使用方法 |
| [rpc/agent/trigger_assignee.go](../rpc/agent/trigger_assignee.go) | 成员读取/原source核对/五类状态 |
| [rpc/agent/trigger_assignee_test.go](../rpc/agent/trigger_assignee_test.go) | 来源/可信字段/状态/实际TLS与限值 |
| [rpc/agent/trigger_client_test.go](../rpc/agent/trigger_client_test.go) | 旧来源client替身声明新未用方法 |
| [docs/trigger-assignee-contract.md](trigger-assignee-contract.md) | 共同契约/九步分工 |
| [docs/trigger-assignee-review.md](trigger-assignee-review.md) | 完整审查/文件/调用链/验证边界 |
| [docs/architecture-decisions.md](architecture-decisions.md) | A60备选/理由/代价/既定确认记录 |
| [docs/project-plan.md](project-plan.md) | 当前已验证成果与下一步 |
| [docs/worktree-collaboration-plan.md](worktree-collaboration-plan.md) | worktree交付/整合/验证记录 |
