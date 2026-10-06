# 阶段7：群历史重新遍历与未读恢复

日期：2026-10-06。共同起点：73ade5b；本批集成分支：codex/stage7-group-unread-recovery。沿已确认A73，不新增协议、表、依赖或权限规则。

## 本轮接口与边界

1. 页面新增“从最新消息重新遍历”按钮，调用 `doRestartTeamGroupHistory()`；内部调用 `loadTeamGroupHistory(true, true)`。原调用及默认参数保持兼容。
2. 第二参数 `restart = false` 仅用于显式重新遍历。重遍历忽略旧结束状态和旧游标，读取最新20条；只有当前身份/范围的有效成功响应才替换分页游标与结束状态。失败、非法响应、过期响应不改变原分页位置。
3. “刷新最新群消息”仍只刷新最新页，不改变旧分页位置。三种操作共享已有在途请求保护、身份/范围世代检查及msg_id去重。成功页交给现有未读控件；GET与重新遍历均不自动确认已读。
4. 迟到的小ID消息可在重新遍历后逐页找到。重遍历不是数据库快照，也不保证持续新增消息期间一次遍历涵盖所有消息；未读数和逐消息凭据仍由IM当前资格检查决定。
5. 提交后丢响应可已有已读凭据。验证先查询再由本人显式重试相同ID，不自动换页或扩大确认范围；重试的SQL不修改首次read_at。测试拦截器只用于模拟响应失败，不进入生产代码。

## 三个执行任务

| Agent任务 | 绝对工作目录 | 分支 | 唯一允许编辑文件 |
| --- | --- | --- | --- |
| A 页面分页 | D:/zy/GoLang/go-im/.worktrees/assignee-backend | codex/stage7-history-restart-page | examples/chat.html、examples/chat.test.cjs |
| B 页面恢复组合 | D:/zy/GoLang/go-im/.worktrees/assignee-gateway | codex/stage7-unread-page-recovery-test | 新examples/team-group-unread-recovery.test.cjs |
| C IM恢复组合 | D:/zy/GoLang/go-im/.worktrees/assignee-ui | codex/stage7-unread-rpc-recovery-test | 新rpc/im/team_group_unread_recovery_test.go |

root统一共同文档、审查、测试、提交及集成，不合main、不push或部署。执行Agent只编辑允许文件和格式/差异检查，不运行test/build或Git写操作；必要时root将已审查的页面提交提供给B。整批最多九步：共同边界、A、B、C、审查整合、集中验证、记录，不凑满九步。

## 验证与限制

页面组合加载实际HTML内联代码与实际未读脚本，HTTP/DOM为替身；IM组合使用生产处理器、本机TCP gRPC，SQL和User资格为替身。真实MySQL首次时间/重启、030迁移、浏览器、Kafka/Compose/云端及真实模型仍留最终统一验收，不能据此标记阶段7或整个项目完成。
