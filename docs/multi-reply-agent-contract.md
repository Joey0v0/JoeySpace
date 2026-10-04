# Agent 逐项回帖闭环共同契约

2026-10-04。沿已确认A37/A41/A44/A46/A47/A50/A55；上轮IM接收完整1a1156b已合本地main。本批九步只实现逐项持久回帖、同步尝试/显式重试RPC、准确只读状态、Gateway重试及本机组合，不接多项页面或群内@AI。

## 1. 兼容、接口与预算

021只把Agent自有agent_task_replies增加默认0的item_index并把主键改成(run_id,item_index)，原msg_id唯一键/正文/范围/accepted保留；015不改，新初始化一致。旧单项SELECT/UPDATE显式限定index0，原插入可沿默认0和原8参数，旧单项Task/Msg键不改。新回帖使用公共model.BotTaskItemMsgID，index0原bot-task:<run>，其余追加:<index>，不使用TaskID替代run。

新增Agent.RetryTaskReplyItem复用GetTaskDraftItemRequest(run_id,显式optional index0..4)，返回GetTaskDraftItemResponse；新HTTP POST /api/v1/agent/runs/:run_id/drafts/:item_index/reply/retry只接受空请求体，身份/范围/内容来自保存记录，JWT原Bearer转发。不收标题、TaskID、Token正文或版本；冻结成功项不会再编辑内容。Agent确认18秒/创建12秒/回帖5秒/IM4秒，显式重试18秒，Gateway重试19秒、逐项确认既有20秒，不改普通路由预算。

## 2. 统一存储与调用边界

共同draftReplyRecord加ItemIndex int32，接口draftCollectionReplyStore规定prepareCollectionReply/loadCollectionReply/acceptCollectionReply；draftItemBotClient只声明IM新方法。实现helper collectionReplyIntent(collection,index)和record.matchesCollection(collection,index)：仅成功目标、正TaskID、固定目标Task键、合法范围/版本/卡片可构造，比对全部run/index/Task/范围/MsgID/content，忽略accepted的推进。等待/创建中/跳过不得写回帖或调用IM。

准备前调用者经draftReader.loadCollection核对User本人、IM当前群资格；prepare短事务用queryDraftCollection(...lock=true,nil)核对完整集合、相同范围/项数和**目标完整taskDraftRun相等**，其他项合法推进不冲突。锁内只读成功结果/旧回帖或插入固定卡片，不调用RPC。目标改变Aborted，已存不同意图AlreadyExists，非法记录/多行/INSERT非1行/提交失败不报告成功。查询新9字段按run/index/actor，读无写、0行未开始、1行必须完整匹配、其他行数拒绝。accept只推进目标accepted，限定完整原记录键/范围/正文；1行成功，0行重读必须完整匹配且accepted=true，>1/错记录拒绝。状态/内容版本和其他项不改，Token不保存。

## 3. 编排与返回事实

ConfigureDraftReplies继续配置原单项replier，同时设置collectionStore与itemBot字段；原旧入口和旧测试替身不调用新方法。draftReplier.attemptCollection(ctx,token,collection,index)先5秒内保存/验证固定记录，再4秒内带原Token调IM PostTaskCreatedCardItem，显式index且消息ID/accepted精确匹配后才保存本地accepted；不得回退旧方法，不调用Task/model。已本地accepted仍先由Reader重新授权，再只读/重放返回，不重复IM。

ConfirmTaskDraftItem仅目标实际首次从非成功得到持久Task成功后尝试回帖；起始已成功的重复确认只读回帖，不静默重发。不确定回帖返回原Task成功＋pending（有持久意图）或unknown（无法确定意图），不能变回waiting/creating或宣称Task失败。原创建预算结束后及时stopCreate，回帖使用独立剩余confirm预算。RetryTaskReplyItem只准本人当前有权的成功目标，不查目标负责人、不重建Task；失败返回适当RPC错误，GET可重读固定pending记录。

集合/指定项GET在授权后只对成功项loadCollectionReply；读取无发布/创建副作用，存储失败返回错误、不伪accepted。disabled/not_started/unknown消息空，pending/accepted必须是该run/index的规范MsgID；unknown仅成功目标可返回，Task已成功和回帖待定分别表达。跳过固定disabled空消息，waiting/creating无回帖记录。编辑/跳过响应仍仅返回目标事实，无须加载其他成功项回帖。

Gateway共享validDraftCollectionItem(item,index,runID)验证目标消息身份和合法状态：pending/accepted/unknown仅succeeded正TaskID；旧disabled/not_started空Msg继续支持；skipped必须disabled。新重试成功只接受succeeded/accepted精确项ID/MsgID，未知/错项结果502；旧字段/状态校验不弱化，ID仍精确字符串。User/IM鉴权、机器人身份、数据归属、至少一次msg_id去重均沿既定方案。

## 4. worktree任务与允许文件

root统一协议/generated、021/init、schema检查(draft_reply_store_test.go)、此契约/共用接口、共同文档和新api/multi_draft_reply_flow_test.go，统一提交/整合/测试。子agent只编辑/gofmt/diffcheck，不go test/build、commit/merge/push，不越界。

- 存储D:/zy/GoLang/go-im/.worktrees/assignee-backend：仅rpc/agent/draft_reply_store.go（index0保护）、新draft_collection_reply_store.go/test；步骤2意图/旧0隔离，步骤3准备/读取/受理事务。不得改root拥有的旧store_test。
- 编排D:/zy/GoLang/go-im/.worktrees/assignee-gateway：仅rpc/agent/draft_reply_rpc.go（配置字段）、draft_collection_confirm_rpc.go、draft_collection_rpc.go、新draft_collection_reply_rpc.go/test，必要draft_collection_confirm_rpc_test.go；步骤4同步回帖，步骤5读取/本人重试。不得改存储/协议/Server其他字段。
- Gateway D:/zy/GoLang/go-im/.worktrees/assignee-ui：仅api新agent_draft_collection_reply.go/test、agent_draft_collection.go/test、agent_draft_collection_confirm.go、agent_draft_collection_edit.go、agent_draft_collection_skip.go、main.go；步骤6HTTP重试，步骤7共享状态/消息身份校验。root新组合文件不越界。

整体9步=共同1＋存储2＋编排2＋Gateway2＋root组合1＋集中审查1。接口先统一，三分支同共同提交开始，按存储→编排→Gateway整合。真实迁移/模型/MySQL/Kafka/容器/浏览器及云端留最终验收，阶段6不误标全部完成。
