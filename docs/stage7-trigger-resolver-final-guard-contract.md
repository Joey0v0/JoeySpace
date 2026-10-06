# 阶段7：后台负责人解析末次资格保护

2026-10-06，从a88ba53建立本轮集成分支。沿用户已确认的A75成员资格版本与IM永久关闭记录，本批只收口User候选解析和IM返回候选前的末次核权。最多六步：共同契约、User解析前后版本、IM解析后User/群复核、独立组合测试、集中验证、记录。无新协议、迁移、依赖、端口或权限模型。

## 固定调用与失败规则

- User ResolveTriggerTeamMember保留原专用mTLS身份、请求/响应、字面姓名匹配、候选限制和原查询。查询前后调用现有CheckTriggerTeamMember，取两次真实正generation；任何一次拒绝或版本不等均不返回候选（不把离队重入误判为连续授权）。候选目标仍限当前active/启用成员；User不额外开放资料。
- IM ResolveTaskTriggerMember先由ReadTaskTriggerContext取得已保存触发范围和受保护的历史，然后仍只对文本中实际出现的名字调用User候选解析。成功结果经原严格回显/候选校验后，IM用持久来源派生的actor/team向User CheckGeneration，接着用同一generation在IM检查当前群成员、team归属和032关闭边界；任一失败无部分候选。最终检查不用请求metadata、候选响应自报范围或旧群成员快照。不持跨RPC SQL锁，不能宣称瞬时原子撤权。
- User/IM现有错误码和安全错误正文沿用；版本不一致为PermissionDenied，零版本和坏存储为Unavailable。请求取消/超时优先按现有规则处理。旧User缺generation直接拒绝；部署仍先核对001/执行031升级User，再执行032升级IM。
- 本批不改ReadTaskTriggerContext已完成的前后核权、普通JWT入口、Agent生成、Push或退出/恢复/清理。真实MySQL、并发、迁移、浏览器和云仍留最终验收。

## 三个独立任务

| 角色 | 绝对工作目录 | 分支 | 唯一允许文件 |
| --- | --- | --- | --- |
| A User查询版本 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-user-resolver-generation | rpc/user/trigger_member.go、trigger_member_test.go、trigger_member_flow_test.go；可新增rpc/user/trigger_member_generation_test.go |
| B IM末次核权 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-im-resolver-final-fence | rpc/im/trigger_assignee.go、trigger_assignee_test.go、trigger_assignee_flow_test.go；可新增rpc/im/trigger_assignee_generation_test.go |
| C 独立组合测试 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-resolver-generation-flow | 仅可新增rpc/im/trigger_resolver_generation_flow_test.go |

root负责本契约、计划/ADR/部署/验收/协作文档、Git保存/整合及所有Go测试。三个worktree从同一共同提交建立干净分支；执行Agent仅编辑允许文件、gofmt/diffcheck，不test/build/Git写或自行合main/push/部署。若测试需要越界修改或改变契约，先向root报告。C的测试可以依赖B约定的最终User CheckGeneration→IM群关闭查询顺序，但不改B文件。未触及允许文件不硬改。
