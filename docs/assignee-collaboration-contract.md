# 负责人选择：并行实施共同契约

日期：2026-10-03。依据用户已讨论的 A49/A50 及本轮明确采用的 worktree 协作方案。本文固定已有方案内的接口细节，不增加业务服务、角色、跨服务事务或后台身份。协议准备不等于功能完成；本轮实际进度见项目计划及最终审查记录。

## 1. 草稿状态与选择

- 保留 `assignee_name` 为最初提取的称呼，即使本人另选成员或明确未指派也不清空。
- 原解析状态保留：空值为旧草稿，`none` 为原文无称呼，`matched` 为唯一匹配，`not_found/ambiguous/truncated` 为待本人处理。
- 复用既有 `assignee_resolution VARCHAR(16)`，手动选择正成员 ID 写 `selected`，明确选择 0 写 `unassigned`；两状态均表示本人已处理。没有新的列或迁移。`selected` 允许原称呼为空但 ID 必须正，`unassigned` ID 必须 0。
- 调用者仍须为原发起人，并拥有当前团队/群资格。选择正 ID 调 User CheckTeamMemberByID，不读取 User 表；不要求目标加入来源群。目录展示不替代当前资格检查。
- 只允许 waiting_confirmation 选择。事务内核对本人、状态、读取的 revision；保存 ID/选择状态的实际变化才 +1。相同选择 no-op 不增加版本。首次把 matched 改为 selected 或歧义改为 unassigned 属于状态改变，版本 +1。原称呼不改变。文字编辑保留选择状态。

## 2. RPC

新增 `SelectTaskDraftAssignee(SelectTaskDraftAssigneeRequest) -> GetTaskDraftResponse`，沿用原 authorization metadata：

| 字段 | 类型 | 必须与含义 |
| --- | --- | --- |
| run_id | int64 | 正数，不能从请求提供团队/发起人 |
| assignee_id | optional int64 | 必须存在，0 为明确未指派，正数为真实成员；负数拒绝 |
| expected_revision | int64 | 正数，必须是本人最近读取的草稿版本 |

ConfirmTaskDraftRequest 追加 `optional int64 expected_assignee_id = 5`，保留已有字段号和版本规则。named、selected、unassigned 草稿必须存在；0 同样是明确审查值。缺失不能默认为 0。若请求存在该字段，任意草稿均要求与锁定后的负责人 ID 相等。

确认规则：

1. waiting_confirmation 的 `not_found/ambiguous/truncated` 必须先选择，不允许直接确认 ID 0；`matched` 可本人确认唯一候选，`selected/unassigned` 可确认保存的选择；旧草稿和 none 保持原兼容。
2. 含称呼或手动选择的草稿确认必须带 reviewed ID。文字/版本/ID 任一不一致返回 Aborted。旧请求缺 reviewed ID 仅能确认旧草稿或 none，不解除旧客户端绕过审查的保护。
3. waiting_confirmation 的正 ID 在冻结前由 User 复核当前成员；Task 在实际创建时仍复核。外部 RPC 不放在数据库事务内，事务内再次核对版本/状态/负责人，防止读取后被修改。
4. creating/succeeded 的本人重试沿用原固定内容/版本/Task 键，不为已冻结负责人做新的选择或重新解析。不得因负责人后续离队而否定已持久成功结果；发起人当前群权限仍必须检查。Task 对不确定创建的幂等重试继续按既有规则。
5. 创建、结果保存、回帖不增加内容版本。原 Task 超时/响应丢失和回帖重试不改变。

无 Token：Unauthenticated；字段不合法：InvalidArgument；无当前资格：PermissionDenied；不存在/非本人：NotFound；过时版本或 reviewed ID：Aborted；冻结后选择/歧义未处理/缺负责人审查：FailedPrecondition；依赖失败：Unavailable/DeadlineExceeded。不得把依赖故障当作未指派。

## 3. Gateway HTTP

GET/PUT/确认/回帖的共享 draft 响应透出 `assignee_name`、`assignee_resolution`。缺失两字段的旧响应仍只按旧契约解释；非法组合不得用来开放确认。ID 和版本仍为十进制字符串。

新增 `PUT /api/v1/agent/runs/:run_id/draft/assignee`，15 秒路由预算：

```json
{"assignee_id":"123","expected_revision":"2"}
```

两个字段必须显式为规范十进制字符串，ID 允许 "0"，版本必须正；不接受数字、null、负数、前导零、额外字段或客户端范围。成功返回完整草稿（含更新版本/原称呼/selected 或 unassigned），RPC 替身异常响应返回 502。

确认正文允许追加字段；新页面始终显式提交它：

```json
{"expected_title":"整理文档","expected_description":"","expected_revision":"3","expected_assignee_id":"123"}
```

Gateway 不决定哪个状态可创建、不操作数据库；有效 reviewed ID 必须透传，确认成功响应 ID 必须与其一致，版本/结果校验继续原规则。HTTP 409 表示冲突/不允许状态，客户端重新读；其他错误沿用现有映射，不泄露 RPC 内部信息。

## 4. 原生页面

- 读取草稿显示原称呼、解析/选择状态和负责人 ID；唯一 matched 必须让用户看到实际人选再确认。无法定位成员名称时显示真实 ID，不能伪造姓名。
- 用既有 `GET /api/v1/teams/:team_id/members?after_user_id=0&limit=100` 目录供选择，支持继续翻页，不能把第一页当完整团队或用目录推断唯一姓名匹配。加载目录是本人显式操作，不自动给所有旧草稿读取增加网络请求。
- 下拉框区分未操作的占位项与明确 "0" 未指派；选择非零需真实目录选项。保留已有 assignee_id 快照，目录加载失败不把其改成零。
- 本人保存选择调用专用 PUT，携带最近 revision。修改文字或负责人后尚未保存，不能确认；文字编辑、选择保存、读取、确认、回帖重试保持互斥。
- 保存返回版本更新快照；过时冲突/结果不确定保留输入并要求先 Load。身份、团队、群、run_id 或上下文 epoch 改变后旧结果不能显示或打开写入。
- 未处理歧义禁用确认；none/旧草稿保留当前操作；matched、selected、unassigned 只有已保存当前内容可确认。新确认正文始终提交 expected_assignee_id，包括 "0"。
- 大整数 ID/版本全程字符串；页面不得再提供任意 ID 输入让用户假冒成员，后端继续真正校验。

## 5. 文件归属与交付

主 agent 独占：协议及生成代码、依赖/迁移/部署、AGENTS.md 与所有共同文档、跨层组合测试。没有新迁移；执行 agent 不改 shared 文件。

- 后端：rpc/agent 中非 pb/非 proto 实现和测试；cmd/agent 中必要启动接线/测试。保留原方法、既有确认/回帖规则，先说明最多三个小目标再实施。
- Gateway：api Go 实现及处理器测试，包括必要 main.go 路由；不修改 examples、rpc 或共同文档。先说明最多两个小目标再实施。
- 页面：仅 examples/chat.html、examples/chat.test.cjs。先说明最多两个小目标再实施。

使用同一协议提交为基线，各自以替身做定向验证。交付实际文件、测试结果、未验证部分及明确提交。主 agent 在集成分支验证完整链后交用户审查，再进入 main；不 push 或部署。真实模型、MySQL 和浏览器验收尚未执行。

## 6. 已定方案内的普通实现取舍

复用解析状态而非新增选择列：A49 已要求保存选择状态，既有列可区分自动与人工且保留原称呼，减少迁移；代价是所有读取/写入合法状态校验需认识新增两状态。追加 optional reviewed ID 而非单独确认 RPC 或布尔开关：保留当前路由与幂等链，并明确区分未审查与审查了零；版本保护整体草稿，ID 仅作为本人已审查负责人的额外契约。复用团队成员分页目录而非增加成员数据副本，最终成员资格继续归 User/Task。均不改变既定服务/权限边界。
