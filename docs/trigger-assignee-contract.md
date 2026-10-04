# 后台触发负责人解析共同契约

2026-10-04，沿用户已确认 A60 mTLS受限服务读取和 A48/A49完整姓名匹配/本人审查；不新增认证、数据归属或自动指派权限。不生成草稿、不接worker、不花模型预算。

## 协议与范围

扩展现有 UserTrigger / IMTrigger 独立TLS服务，不改普通Bearer服务或机器人写端口。User新增 ResolveTriggerTeamMember(actor_id,team_id,name)，返回echo actor/team/name、候选user_id/username/nickname和truncated，无role/密码/Token。IM新增 ResolveTaskTriggerMember(message_id,name)，不允许自报actor/team/group，返回保存message/actor/team/group/request_key/name及候选。用户名/昵称只为核对完整匹配结果，不暴露其他资料。

请求name须已经trim、合法UTF8、1—64字符；RPC不模糊/分词/大小写归一。匹配沿A48 CAST AS BINARY 的完整用户名或昵称OR，不偏爱用户名，用户ID升序，每成员一次；SQL最多21条，返回20并标truncated，只有未截断恰一名才是matched。共享internal/model/agent_trigger_member.go只校验字面形状，scope、次数/顺序仍在每层独立检查。候选字段UTF8、username非空且≤64字符、nickname≤64字符；任意NULL/不匹配/重复/乱序/超过21候选拒绝，不静默变未指派。新响应预算32KiB，旧资格响应仍4KiB限制，不扩大其读取。

## 各层实现

1. User先RequireServiceIdentity精确IM SAN，nil/明文/伪metadata不得查库；检验输入后总3秒。复用CheckTriggerTeamMember检查actor当前团队与启用账户，查候选SQL仍限定该团队、候选启用且包含actor当前启用成员EXISTS；返回前再次核对actor当前资格，空结果不得把已撤权当not_found。显式Rows.Scan全21条严格校验，安全错误/ctx优先，状态不缓存。普通User.ResolveTeamMember完全保留。
2. IM新增方法先精确Agent鉴别和输入，总5秒。调用既有ReadTaskTriggerContext取得已保存来源、团队群、原作者、最多20条且ID≤来源的当前可读文本；name必须字面出现在instruction或content_type=1文本，沿旧assigneeMention规则，不把卡片/图片当证据。只有teams实现新triggerMemberResolver能力才可解析。调用Resolve(ctx,savedActor,savedTeam,name)，严格echo、大小/顺序/匹配检查；返回前重查保存当前group membership。无伪Token/跨库，无新增连接/配置。缺能力、旧User未实现或失败拒绝，不回退普通Bearer。
3. IM的triggerTeamClient新Resolve方法复用现有conn；2秒子预算、清空全部outgoing metadata、禁RPC自动重试。单调用MaxCallRecvMsgSize32KiB及proto.Size检查；原Check4KiB行为不变。关闭、caller取消/超时优先；预期PermissionDenied/Unauthenticated/InvalidArgument/取消超时保留，其他safeUnavailable。
4. Agent的TriggerContextClient新ResolveMember(ctx,messageID,name)复用conn，5秒、清metadata、单调用32KiB，完整echo/正范围/固定key/候选/大小检查，无RPC重试。私有resolveAssignee(ctx,source,draft)先严格validTriggerContext原输入、模型不许给ID/状态，trim字面并验证来源；非空调用新RPC，响应scope必须完全等于已核实source。空称呼none不调用，空/多/截断保留not_found/ambiguous/truncated，唯一保存matched候选仍待本人审查。只改草稿Assignee相关字段，正文/时间依据保留；错误返回零草稿，不标成功。旧本人Bearer resolver/preparer不改，本批私有adapter未接后台生成。

认证/撤权错误在触发请求上返回相应状态；SQL错误、非法成功包不泄露名称/正文/凭据。每次检查当前资格，不承诺跨服务检查为分布式事务或消除返回后的撤权窗口。

## 九步和允许文件

1 root共同协议/生成、形状helper、IM能力接口、旧client测试interface声明与此契约。已有protoc31.1/plugins，不升级依赖，不删pb。
2—3 backend，D:/zy/GoLang/go-im/.worktrees/assignee-backend：仅新rpc/user/trigger_member.go/test.go；当前资格+严格21条匹配及测试。
4—5 gateway，D:/zy/GoLang/go-im/.worktrees/assignee-gateway：仅新rpc/im/trigger_assignee.go/test.go、trigger_member_client.go/test.go；保存范围/来源文字检查及User客户端/故障测试。
6—7 ui，D:/zy/GoLang/go-im/.worktrees/assignee-ui：仅新rpc/agent/trigger_assignee.go/test.go；新client/严格scope及草稿状态转换测试。
8 root生产TLS/SQL替身跨服务组合、共享helper/协议测试；root可新增rpc/im/trigger_assignee_flow_test.go、rpc/user/trigger_member_flow_test.go、internal/model/agent_trigger_member_test.go。
9 root集中Go/Linux验证、ADR/计划/协作和全文件审查。

子agent只编辑/gofmt/diffcheck，不运行test/build/审批/提交/合main/push。root统一Git/协议/集中验证；复用现有监听注册自动包含新RPC，本批不改main、listener、配置、迁移、页面或旧业务。数据库/证书/容器/模型/云最终统一验收。
