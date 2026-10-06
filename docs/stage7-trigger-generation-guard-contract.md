# 阶段7：后台Agent触发上下文代际核权

2026-10-06，起点25de3fc，集成codex/stage7-trigger-generation-guard。沿用户确认A75的User成员资格版本与IM持久关闭记录，把后台Agent的触发上下文读取接入同一代际规则。本批不开放退出/清理RPC，不改变mTLS身份、Kakfa事件格式、触发保存范围、普通JWT入口、Push或页面。最多六步：共同协议/契约、User响应、IM客户端、IM触发上下文、整合验证、记录。

## 固定协议和处理顺序

- UserTrigger.CheckTriggerTeamMemberResponse新增field3 `generation`（int64），保留actor/team原字段与方法。旧调用端忽略新增字段；新IM必须要求正generation，拒绝旧User的0响应，部署User先升级。User只为活动成员且启用账户返回其DB当前版本，不从RPC入参自报版本或JWT提取，不回资料/角色。
- IM的triggerTeamClient保留`Check(ctx,actor,team) error`用于旧调用方和现有测试，新增`CheckGeneration(ctx,actor,team)(int64,error)`。二者都走原专用mTLS、只派生持久trigger的actor/team、不转发用户Token；Check复用CheckGeneration，nil/范围不符/版本<=0固定Unavailable。错误码映射沿旧逻辑。
- root在triggerTeamEligibility增加CheckGeneration方法，执行Agent不改该共同接口。测试替身可为旧有成功路径返回固定正1，生产禁止默认1。User返回大版本如9007199254740993须精确保留。
- ReadTaskTriggerContext仍先从IM持久Outbox/原消息派生固定范围并查一次当前群资格，再CheckGeneration；随后使用同一条IM新读SQL检查当前group_members/群team/closedThrough<generation，才读取历史。历史形成后再次CheckGeneration，要求与前一次完全相同，随后再次检查IM当前群成员/关闭边界才返回；版本改变或已关闭拒绝，不返回部分历史。SQL和User服务之间不持锁，跨服务撤权仍非瞬时原子。
- ResolveTaskTriggerMember通过ReadTaskTriggerContext获得受保护的来源；其后候选解析和末次群检查本批保持既有行为，尚未把末次校验纳入代际一致性。下一批收口这一单独路径和User Resolver前后版本比较，不把本批说成后台所有读取完成。
- SQL/User和Kafka仍替身；真实MySQL/031/032/mTLS部署证书/浏览器/模型和云仍最终验收。无新迁移、端口或密钥。

## 三个执行任务

| 角色 | 绝对工作目录 | 分支 | 唯一允许文件 |
| --- | --- | --- | --- |
| A User版本响应 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-user-trigger-generation | rpc/user/trigger_team.go、trigger_team_test.go、trigger_member_test.go、trigger_member_flow_test.go、trigger_tls_flow_test.go、trigger_membership_active_test.go；可新增rpc/user/trigger_generation_test.go |
| B IM专用客户端 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-trigger-client-generation | rpc/im/trigger_team_client.go、trigger_team_client_test.go；可新增rpc/im/trigger_client_generation_test.go |
| C IM触发上下文 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-trigger-context-generation | rpc/im/trigger_context.go、trigger_context_test.go、trigger_context_flow_test.go、trigger_compose_test.go、trigger_listener_test.go、trigger_assignee_test.go、trigger_assignee_flow_test.go；可新增rpc/im/trigger_context_generation_test.go |

root拥有trigger.proto/生成、trigger_context_contract.go、本契约、Git/集中测试/文档。执行Agent仅编辑/gofmt/diffcheck，不test/build/Git写/mainmerge/push/部署；不越界到普通IM/Agent/User其他方法/生成文件。若发现契约或权限模型冲突，暂停受影响改动报告root。三个worktree从共同提交建干净独立分支。其余未碰文件不因允许范围而硬改。
