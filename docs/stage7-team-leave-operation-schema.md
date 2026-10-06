# 阶段7：User退出操作持久表基础

2026-10-06，从b843b09建立codex/stage7-team-leave-operation-schema。沿用户已确认的A75/A77：本人非拥有者退出时，User须先在本地事务撤权并固定退出操作，随后通过受限mTLS调用IM清理；中断后本人沿同一请求重试。此小步只准备User拥有的操作表，不开放RPC/HTTP，不修改成员状态，也不调用IM。

033需在001和031后对已有User数据库执行一次；新库init包含相同DDL。`user_team_leave_operations`使用自增内部`id`、固定team/user、大小写敏感ASCII `request_key`、退出时正`generation`、状态0待IM清理/1完成与完成时间。`(user_id,request_key)`唯一，使本人重试可找到原操作且跨团队复用同键不能静默变成另一操作；`(team_id,user_id,generation)`唯一，使同一资格代际只能有一笔退出。非正范围与状态/完成时间矛盾由数据库拒绝。退出记录不外键级联到team_members，避免成员生命周期变化删除恢复依据；User后续代码仍须核对本人、固定范围和状态，唯一键自身不是身份授权。

本批不预先为历史成员创建操作，也不把033的表存在视为退出完成。后续分步实现User事务写入/查询与幂等、IM受限清理入口、User完成记录/显式恢复、重入及Push核权。跨服务没有原子事务；User撤权事务提交后、IM清理前的中断必须保持状态0并允许本人重试，清理完成但回包丢失也沿同操作重试。旧群消息、离线和阅读记录不由本表删除。

备选用随机操作ID同时充当请求键：客户端重试丢失原ID时难以找到操作；只在team_members上留状态/版本而不保存固定请求键：无法区分同一重试与新请求。A75已经选择持久固定操作，本批选双唯一键和User拥有数据，不增加新的架构选择。代价为033一次迁移和后续事务/接口实现，真实MySQL迁移与锁行为尚未验收。

## 本步实现与审查

只做一个目标：为“先撤权、后调用IM，失败由本人按同操作重试”提供不会随成员状态消失的User持久记录。实际未新增运行时调用或外部API；改动是033增量迁移、新库init同表以及范围/部署文档。静态检查先因两份文件的CRLF/LF差异误报，标准化换行后提取两份建表语句得到相同966字符；`git diff --check`通过。当前机器没有可用mysql或docker命令，真实DDL执行、唯一约束行为和事务竞争未验证；本步未运行Go/Node测试，因为未改程序。

相对b843b09实际修改共7份：

| 文件定位 | 本步作用 |
| --- | --- |
| [deploy/mysql/migrations/033_user_team_leave_operations.sql](D:/zy/GoLang/go-im/deploy/mysql/migrations/033_user_team_leave_operations.sql) | 旧库一次性User操作表增量迁移 |
| [deploy/mysql/init.sql](D:/zy/GoLang/go-im/deploy/mysql/init.sql) | 新库同一建表定义 |
| [deploy/README.md](D:/zy/GoLang/go-im/deploy/README.md) | 001→031→033顺序和未执行边界 |
| [docs/architecture-decisions.md](D:/zy/GoLang/go-im/docs/architecture-decisions.md) | A75本步存储取舍与代价 |
| [docs/project-plan.md](D:/zy/GoLang/go-im/docs/project-plan.md) | 当前进度与下一小步 |
| [docs/stage7-acceptance.md](D:/zy/GoLang/go-im/docs/stage7-acceptance.md) | 最终真实数据库核对项 |
| [docs/stage7-team-leave-operation-schema.md](D:/zy/GoLang/go-im/docs/stage7-team-leave-operation-schema.md) | 本步契约、验证与全部文件 |
