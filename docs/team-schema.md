# 阶段 2：团队数据与接口

迁移文件：[001_teams.sql](../deploy/mysql/migrations/001_teams.sql)。它为现有 `go_im` 数据库新增 `teams` 和 `team_members` 两张表，不修改旧 `groups`、`group_members`。团队表示协作成员与权限范围；群表示聊天会话，两者不是同一个对象。一个用户可以加入多个团队；以后一个团队可以有多个群。

| 表 | 关键字段与约束 | 用途 |
| --- | --- | --- |
| `teams` | `id`、`name`、`owner_id`；拥有者必须是现有用户 | 保存团队本身 |
| `team_members` | `(team_id, user_id)` 主键；`role` 为 0 成员、1 管理员、2 拥有者 | 限定团队成员和基础角色，防止同一用户重复加入 |

当前权限范围：拥有者（2）可添加成员、查看列表、调整普通成员与管理员角色；管理员（1）和普通成员（0）目前都只可查看成员列表。管理员后续能管理哪些资源，将随对应业务小步实现，不能因为角色名就默认已有管理权限。

“创建团队”RPC 已在用户与团队服务的**同一数据库事务**中插入团队和创建者的拥有者成员记录；这样任一步失败都不会留下半个团队。数据库外键保证引用的用户和团队存在，但“`owner_id` 同时有一条角色为 2 的成员记录”仍由业务事务维护。请求只包含团队名称，创建者由 RPC 验证登录 Token、查询可用账户后确定；成功时返回团队 ID。

HTTP 入口现为 `POST /api/v1/teams`，请求头需有 `Authorization: Bearer <登录 Token>`，JSON 请求体只含 `{"name":"项目团队"}`。成功时返回 `{"code":0,"msg":"success","data":{"team_id":"..."}}`；ID 用字符串避免浏览器数字精度损失。调用链为 HTTP API → 用户与团队 RPC 验证 Token、读取账户 → MySQL 事务写入 `teams` 和 `team_members`。API 不接收 `owner_id`，也不直接写数据库。

新增 `AddTeamMember(team_id, user_id)` RPC，当前采用**拥有者直接添加**规则：RPC 验证操作人的 Token 和账户状态，再查询其在目标团队的角色；只有角色 2 可以添加。目标用户必须存在且可用，新成员写入角色 0。重复加入由 `(team_id, user_id)` 主键阻止并返回 `AlreadyExists`。普通成员、管理员和非成员均不能添加。此步没有邀请接受流程。

添加成员的 HTTP 入口为 `POST /api/v1/teams/{team_id}/members`，同样需要 Bearer Token，请求体为 `{"user_id":"目标用户ID"}`。路径中的团队 ID 和请求体中的用户 ID 都使用十进制字符串，避免浏览器大整数精度损失。成功返回 `{"code":0,"msg":"success"}`；非拥有者返回 403，目标用户不存在返回 404，目标用户禁用或重复加入返回 409，RPC 不可用返回 503。API 只校验格式和转发身份，添加权限由 RPC 判断。

`ListTeamMembers(team_id, after_user_id, limit)` RPC 只允许团队成员读取。服务先验证 Token 和账户状态，再检查该用户属于目标团队，之后才联查成员与用户资料，返回用户 ID、用户名、昵称和角色；非成员返回 `PermissionDenied`，不执行列表查询。结果按用户 ID 升序；`limit` 省略时为 20，最大 100。第一页的 `after_user_id` 为 0；响应中的 `next_after_user_id` 非 0 时可用于请求下一页，0 表示已到末页。

列表 HTTP 入口为 `GET /api/v1/teams/{team_id}/members?after_user_id=0&limit=20`，需携带 Bearer Token。两个分页参数可省略；`after_user_id` 使用十进制字符串，`limit` 为 1～100 的整数。成功时 `data` 包含 `members` 数组和 `next_after_user_id`；用户 ID 与下一页游标均以字符串形式返回。空页返回 `"members":[]` 和游标 `"0"`。非成员返回 403，RPC 不可用返回 503；API 只校验格式，读取权限仍由 RPC 判断。

`SetTeamMemberRole(team_id, user_id, role)` RPC 仅允许拥有者调用；目标必须已是团队成员，`role` 只能为 0 或 1。不能变更拥有者角色，也不能通过此方法创建另一个拥有者。目标角色已经相同时直接返回成功；更新时再次限定旧角色为 0 或 1，避免将已变化的拥有者记录误改。

`AuthorizeTeamGroupCreation(team_id)` 是供未来 IM 服务调用的内部 RPC。它从登录 Token 查询可用账户，再检查该账户在目标团队是否为拥有者；普通成员、管理员和非成员均返回 `PermissionDenied`，数据库故障返回 `Unavailable`。本步只有授权检查，尚未接入 IM 群创建。

`CheckTeamMember(team_id)` 是供 IM 等业务服务检查当前团队资格的内部 RPC。它从登录 Token 确定当前可用用户，查询其是否仍有目标团队成员记录；拥有者、管理员和普通成员均可通过，非成员返回 `PermissionDenied`。成功响应现返回该用户的 `user_id` 与当前团队 `role`，供后续任务服务判断操作权限；IM 已使用此方法检查团队资格，但忽略新增字段。被指派人的成员资格不由这个只验证调用者的接口判断。

`CheckTeamMemberByID(team_id, user_id)` 用于需要核对另一名成员的业务服务。只有当前团队成员可以查询；服务再检查目标用户在同一团队的成员记录和账号可用状态。目标不属于团队返回 `NotFound`，目标账号停用返回 `FailedPrecondition`，数据库故障不放行。后续任务服务可用它核对负责人，不直接读取用户与团队服务的表。

角色变更 HTTP 入口为 `PUT /api/v1/teams/{team_id}/members/{user_id}/role`，需携带 Bearer Token，请求体为 `{"role":1}`（管理员）或 `{"role":0}`（普通成员）。路径中的 ID 使用十进制字符串。成功返回 `{"code":0,"msg":"success"}`；非拥有者返回 403，目标成员不存在返回 404，拥有者角色不可变或成员状态发生变化返回 409。API 只校验格式并转发，权限和更新条件仍由 RPC 判断。

迁移脚本暂未执行。本地旧数据库和云端已有数据卷不会自动运行 Docker 的首次初始化脚本；整体代码完成并统一同步部署时，先核对实际数据库和备份，再对已有 `go_im` 数据库单独执行迁移。新建数据库也需要执行此迁移，因为它尚未加入旧 `init.sql`。目前 SQL 测试替身验证了创建事务、添加成员的权限与重复加入、成员列表的权限和分页，以及角色变更的权限与更新条件；本机 gRPC 调用验证了契约，HTTP 测试替身验证了创建、添加、列表与角色变更入口的请求转发及错误映射；未连接真实 MySQL，也未运行完整 HTTP → RPC → MySQL 链路。
