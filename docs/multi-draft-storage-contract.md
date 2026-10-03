# 多项草稿保存与读取：共同契约

2026-10-04，按已确认 A37/A50/A54—A56。上一批准备结果 `5ef2ff8` 已合 main。本批仅接一次生成、原子保存、集合/项读取、旧单项保护；不接多项编辑/确认/跳过/回帖或页面。主 agent 拥有协议/生成目录、019初始化/迁移、文档；三个执行角色独立 worktree。

## 1. 持久与兼容

沿现有两表，不新建草稿库/JSON表。agent_runs新增draft_mode(single/collection)、item_count(1..5)，旧默认single/1；agent_task_drafts新增status，旧默认空表示仅从旧run读取权威状态，不回填或改旧创建键/结果。新collection即使仅一项也mode=collection，原子保存0..N-1稳定索引，各项status=waiting_confirmation、revision默认1，独立完整负责人/时间依据，Task键空/ID0。运行初始status=waiting_confirmation，但本批不据其宣称任务已创建；集合接口没有整轮成功状态。019须018后、升级Agent前执行，本批不执行真实迁移。

旧单项所有读/写/确认/回帖必须拒绝collection（含1项）。建议旧共同SELECT的status用CASE WHEN r.draft_mode='single' THEN r.status ELSE 'collection' END AS status，保留旧字段/SQL参数；loadDraftForInitiator及lockedDraft发现collection返回FailedPrecondition，不暴露首项、不调用写服务。仅加查询子句导致读不到时不能把collection默认为第0项。旧插入、指纹、单项状态/版本、Task键和消息键逐字保留。集合读取只接受collection，single返回FailedPrecondition，避免两种调用意图混同；下一批逐项写接口显式项身份，不靠普通int默认0。

## 2. 生成与事务

新PrepareTaskDraftCollection复用原请求字段/Token/幂等请求键/20秒总预算，ConfigureDraftPreparation直接接已有Eino GenerateDrafts，不再给Server另配模型或外部账号。本人User身份、IM当前团队群授权在重放查询前；保存的同键结果优先，不重模型/时间解释。

collection指纹精确JSON字段顺序为team_id、group_id、instruction(TrimSpace)、mode:"collection"、instruction_reference_unix_ms(指针omitempty)，再SHA256小写hex。同本人键命名空间仍共用，old/new模式、范围/指令/参考改变必须AlreadyExists，不返回另一种运行。旧指纹不加mode字段。

模型生成1..5项后用既有verifyGeneratedDrafts全项核验，再以newWaitingTaskDraftRun规范标题/说明；不改原模型证据。所有项均校验后在一个短事务写运行及逐项草稿，任何INSERT失败回滚；只读外部RPC/模型不占SQL锁。唯一键冲突用现有findExistingDraft同内容返回赢家运行，不重复保存/重编号；也不能对任意1062盲当成功。相同并发可能重复调用模型，不承诺只计费一次。后续增删/重排不在本版范围。

固定INSERT供跨层SQL替身核对：运行列(id,team_id,group_id,initiator_id,request_key,request_fingerprint,status,draft_mode,item_count)为9参数；status waiting_confirmation、mode collection。草稿列(run_id,item_index,title,description,assignee_id,due_at_unix_ms,source_message_id,assignee_name,assignee_resolution,deadline_text,deadline_source,deadline_source_message_id,deadline_reference_unix_ms,deadline_timezone,deadline_resolution,deadline_reason,deadline_parsed_unix_ms,instruction_reference_unix_ms,status)为19参数，revision沿数据库默认1。不得静默忽略后项或改变来源ID。

## 3. RPC与读取校验

新增PrepareTaskDraftCollection(原PrepareTaskDraftRequest)->原PrepareTaskDraftResponse(run_id)，GetTaskDraftCollection(原GetTaskDraftRequest)->GetTaskDraftCollectionResponse，GetTaskDraftItem(GetTaskDraftItemRequest)->GetTaskDraftItemResponse。后者item_index为optional int32，必须存在且0..4，不能缺值默认为0。

新集合响应run/team/group正ID，item_count=1..5，items按0..N-1返回完整集合；单项响应同样带run/team/group/count及item，索引与请求一致且小于count。TaskDraftCollectionItem带optional item_index(必须存在)、status、draft(已有TaskDraftItem)、task_id、reply_status、reply_msg_id。本批新项只能waiting_confirmation/task_id0，reply_status disabled或not_started/msg空；后续creating/succeeded/skipped及逐项回帖另批接，不提前返回假成功。新draft版本必须正，负责人resolution必须非空，deadline必须完整新九字段，不能把损坏列伪装legacy。

数据库读取按run/本人过滤、验证mode/count、全部项的scope相同、索引唯一连续且数量等于count，每项字段合法/规范、版本正、显式waiting状态、键/ID空。可以单SELECT JOIN并LIMIT6检测超限，不能LIMIT5默默截断。缺失/其他本人NotFound，损坏记录Unavailable，模式不符FailedPrecondition。指定项可先完整读小集合再选，超出实际count为NotFound。User身份和IM当前团队群权限仍每次核对，拒绝不得返回任意项。两个新RPC在ConfigureDraftAccess后自动可用，不新增进程参数。

## 4. HTTP契约

- POST /api/v1/teams/:team_id/groups/:group_id/task-draft-collections，原生成body/token/Idempotency-Key校验及响应{code,msg,data:{run_id:"..."}}，同生成22秒路由预算。
- GET /api/v1/agent/runs/:run_id/drafts，数据{run_id,team_id,group_id,item_count,items}。
- GET /api/v1/agent/runs/:run_id/drafts/:item_index，数据{run_id,team_id,group_id,item_count,item}，两种读取15秒预算。

所有int64 ID和revision用JSON十进制字符串，item_index/item_count是小整数，包括第0项也输出index=0。各项{item_index,status,draft,task_id:"0",reply_status,reply_msg_id}，draft复用已有字段。读取不得接受缺索引、空/部分集合、重复/错序/越界索引、run范围不符、非正版本、坏文字、空负责人状态或缺deadline/异常证据。新项waiting且task_id0及disabled/not_started/空msg；共享现有负责人/时间验证但新对象不能退化legacy。异常服务响应502；权限403，模式/指纹冲突409，其余沿draftRPCError。HTTP只转发原Token和稳定身份，不直接读DB或模型，不把旧单项路由改成集合入口。

## 5. 执行角色与步骤

主 agent共同准备1、后端3（生成事务、集合/项读取与资格、旧保护）、Gateway2（生成、读取与响应验证）、组合测试1、集中审查1，全批最多8步。角色先读本契约与计划，每小步先解释、普通细节继续实施，发现新架构/权限选择停止报告。

后端只改rpc/agent非生成实现及相应测试（允许新增collection小文件，旧文件只作必要接线/防护），不改api/协议/迁移/docs/依赖。Gateway只改api生成/读取处理器、新测试、main.go必要路由，不改api/multi_draft_persistence_flow_test.go。组合角色只新增该api组合文件，可复用原flow fixture、构造真实本机HTTP/TCP gRPC生产Agent，User/IM/Eino/SQL为替身；此角色本批不改页面。不要自行commit/merge/push/执行迁移/请求模型。

所有测试由主 agent集中执行，执行agent完成代码/gofmt/diffcheck即交付，避免子agent测试审批等待。组合测试覆盖新模式fingerprint、全部九时间字段/成员/大ID实写实读、指定第0/第1、同键重放/冲突、旧接口拒绝collection、当前资格撤销、坏后项无保存/无部分成功，既有单项回归保留；后端存储测试补事务失败/并发重复赢家、缺项/损坏/模式拒绝。完成后全量Go、相关页面回归和Linux Agent/Gateway构建；真实数据库/模型/浏览器/容器仍未验收。
