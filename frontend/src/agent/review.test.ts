import test from 'node:test'
import assert from 'node:assert/strict'
import { createSession } from '../auth/session.ts'
import { ApiError } from '../api/client.ts'
import type { createAgentApi, TriggerScope } from './api.ts'
import type { AgentTrigger, DraftCollection, DraftItem } from './model.ts'
import { createAgentReview, initialAgentReviewState } from './review.ts'

const group = { teamId: '9007199254740993', groupId: '9' }
const source: TriggerScope = { ...group, messageId: '9007199254740995' }
const status = (value: AgentTrigger['status'], run_id = '0'): AgentTrigger => ({ message_id: source.messageId, team_id: group.teamId, group_id: group.groupId, status: value, run_id })
function harness() {
  const identity = createSession()
  identity.setSession('one')
  const state = initialAgentReviewState()
  const asks: Array<{ resolve: (value: { answer: string }) => void; reject: (error: unknown) => void }> = []
  const triggers: Array<{ scope: TriggerScope; resolve: (value: AgentTrigger) => void; reject: (error: unknown) => void }> = []
  const collections: Array<{ resolve: (value: DraftCollection) => void; reject: (error: unknown) => void }> = []
  const items: Array<{ index: number; resolve: (value: DraftItem) => void; reject: (error: unknown) => void }> = []
  const writes: Array<{ kind: string; index: number; body: unknown; resolve: (value: DraftItem) => void; reject: (error: unknown) => void }> = []
  const decisions: Array<{ kind: string; index: number; body: unknown; resolve: (value: DraftItem) => void; reject: (error: unknown) => void }> = []
  let now = 0
  let nextTimer = 0
  const timers = new Map<number, { due: number; callback: () => void }>()
  const api = {
    ask: () => new Promise<{ answer: string }>((resolve, reject) => { asks.push({ resolve, reject }) }),
    trigger: (scope: TriggerScope) => new Promise<AgentTrigger>((resolve, reject) => { triggers.push({ scope, resolve, reject }) }),
    collection: () => new Promise<DraftCollection>((resolve, reject) => { collections.push({ resolve, reject }) }),
    item: (_scope: unknown, index: number) => new Promise<DraftItem>((resolve, reject) => { items.push({ index, resolve, reject }) }),
    editText: (_scope: unknown, index: number, body: unknown) => new Promise<DraftItem>((resolve, reject) => { writes.push({ kind: 'text', index, body, resolve, reject }) }),
    selectAssignee: (_scope: unknown, index: number, body: unknown) => new Promise<DraftItem>((resolve, reject) => { writes.push({ kind: 'assignee', index, body, resolve, reject }) }),
    editDeadline: (_scope: unknown, index: number, body: unknown) => new Promise<DraftItem>((resolve, reject) => { writes.push({ kind: 'deadline', index, body, resolve, reject }) }),
    confirm: (_scope: unknown, index: number, body: unknown) => new Promise<DraftItem>((resolve, reject) => { decisions.push({ kind: 'confirm', index, body, resolve, reject }) }),
    skip: (_scope: unknown, index: number, body: unknown) => new Promise<DraftItem>((resolve, reject) => { decisions.push({ kind: 'skip', index, body, resolve, reject }) }),
    retryReply: (_scope: unknown, index: number) => new Promise<DraftItem>((resolve, reject) => { decisions.push({ kind: 'reply', index, body: null, resolve, reject }) }),
  } as unknown as ReturnType<typeof createAgentApi>
  const review = createAgentReview(api, identity, state, {
    now: () => now,
    setTimeout: (callback, delay) => { const id = ++nextTimer; timers.set(id, { due: now + delay, callback }); return id as unknown as ReturnType<typeof setTimeout> },
    clearTimeout: id => { timers.delete(id as unknown as number) },
  })
  const advance = (ms: number) => {
    const target = now + ms
    while (true) {
      const entry = [...timers].sort((a, b) => a[1].due - b[1].due)[0]
      if (!entry || entry[1].due > target) break
      now = entry[1].due; timers.delete(entry[0]); entry[1].callback()
    }
    now = target
  }
  return { identity, state, asks, triggers, collections, items, writes, decisions, timers, review, advance }
}

const draftItem = (item_index: number, status: DraftItem['status'] = 'waiting_confirmation'): DraftItem => ({
  item_index, status, task_id: status === 'succeeded' ? '12' : '0', reply_status: 'disabled', reply_msg_id: '',
  draft: { revision: '1', title: '任务', description: '', assignee_id: '0', assignee_name: '', assignee_resolution: 'none', due_at_unix_ms: 0, source_message_id: '8', deadline: { text: '', source: 'none', source_message_id: '0', reference_unix_ms: 0, timezone: 'Asia/Shanghai', resolution: 'none', reason: '', parsed_unix_ms: 0, instruction_reference_unix_ms: 0 } },
})
const draftCollection = (items: DraftItem[], runId = '7'): DraftCollection => ({ run_id: runId, team_id: group.teamId, group_id: group.groupId, item_count: items.length, items })

test('Ask is temporary, uncertain failure retains question, and closed old replies cannot reappear', async () => {
  const h = harness()
  h.review.openAsk(group)
  const first = h.review.ask('  问题一  ')
  assert.equal(h.state.question, '问题一')
  h.asks[0]!.reject(new ApiError(504, 'timeout'))
  await first
  assert.match(h.state.askError, /结果未确认/)
  assert.equal(h.state.answer, '')
  const second = h.review.ask('问题二')
  h.review.close()
  h.asks[1]!.resolve({ answer: '旧答案' })
  await second
  assert.equal(h.state.mode, 'closed')
  assert.equal(h.state.answer, '')
  h.review.dispose()
})

test('queued and running poll one request at a time, then completed stops and keeps run ID', async () => {
  const h = harness()
  const first = h.review.openTrigger(source)
  assert.deepEqual(h.triggers[0]!.scope, source)
  h.triggers[0]!.resolve(status('queued'))
  await first
  assert.equal(h.state.trigger?.status, 'queued')
  h.advance(4999); assert.equal(h.triggers.length, 1)
  h.advance(1); assert.equal(h.triggers.length, 2)
  h.advance(30000); assert.equal(h.triggers.length, 2)
  h.triggers[1]!.resolve(status('running'))
  await Promise.resolve(); await Promise.resolve()
  h.advance(5000); assert.equal(h.triggers.length, 3)
  h.triggers[2]!.resolve(status('completed', '9007199254740997'))
  await Promise.resolve(); await Promise.resolve()
  assert.equal(h.state.trigger?.run_id, '9007199254740997')
  assert.equal(h.timers.size, 0)
  h.advance(30000); assert.equal(h.triggers.length, 3)
  h.review.dispose()
})

test('polling slows after two minutes, stops after ten, and manual refresh remains', async () => {
  const h = harness()
  const first = h.review.openTrigger(source)
  h.triggers[0]!.resolve(status('queued')); await first
  h.advance(120000)
  h.triggers[1]!.resolve(status('queued')); await Promise.resolve(); await Promise.resolve()
  h.advance(14999); assert.equal(h.triggers.length, 2)
  h.advance(1); assert.equal(h.triggers.length, 3)
  h.triggers[2]!.resolve(status('queued')); await Promise.resolve(); await Promise.resolve()
  h.advance(465000)
  h.triggers[3]!.resolve(status('queued')); await Promise.resolve(); await Promise.resolve()
  assert.equal(h.timers.size, 0)
  const manual = h.review.refreshTrigger()
  assert.equal(h.triggers.length, 5)
  h.triggers[4]!.resolve(status('running')); await manual
  assert.equal((h.state.trigger as AgentTrigger | null)?.status, 'running')
  assert.equal(h.timers.size, 0)
  h.review.dispose()
})

test('scope and identity changes discard in-flight Ask and trigger results', async () => {
  const h = harness()
  const oldTrigger = h.review.openTrigger(source)
  const newer = h.review.openTrigger({ ...source, messageId: '8' })
  h.triggers[0]!.resolve(status('completed', '7')); await oldTrigger
  assert.equal(h.state.trigger, null)
  h.triggers[1]!.resolve({ ...status('running'), message_id: '8' }); await newer
  assert.equal((h.state.trigger as AgentTrigger | null)?.status, 'running')
  h.identity.setSession('two')
  assert.equal(h.state.mode, 'closed')
  assert.equal(h.state.trigger, null)
  assert.equal(h.timers.size, 0)
  h.review.dispose()
})

test('403/404 clear private trigger state while service errors preserve safe status', async () => {
  const h = harness()
  const first = h.review.openTrigger(source)
  h.triggers[0]!.resolve(status('queued')); await first
  const temporary = h.review.refreshTrigger()
  h.triggers[1]!.reject(new ApiError(503, 'private upstream detail')); await temporary
  assert.equal(h.state.trigger?.status, 'queued')
  assert.match(h.state.triggerError, /稍后/)
  const denied = h.review.refreshTrigger()
  h.triggers[2]!.reject(new ApiError(403, 'private authorization detail')); await denied
  assert.equal(h.state.mode, 'closed')
  assert.equal(h.state.trigger, null)
  assert.equal(h.timers.size, 0)
  h.review.dispose()
})

test('completed trigger alone permits collection, with independent item counts and selection', async () => {
  const h = harness()
  h.review.openAsk(group)
  await h.review.loadCollection()
  assert.equal(h.collections.length, 0)
  const trigger = h.review.openTrigger(source)
  h.triggers[0]!.resolve(status('completed', '7')); await trigger
  const load = h.review.loadCollection()
  assert.equal(h.collections.length, 1)
  h.collections[0]!.resolve(draftCollection([draftItem(0), draftItem(1, 'succeeded'), draftItem(2, 'skipped')]))
  await load
  assert.deepEqual(h.state.counts, { waiting: 1, created: 1, skipped: 1 })
  h.review.selectItem(1)
  assert.equal(h.state.selectedIndex, 1)
  assert.equal(h.state.collection?.items[1]?.task_id, '12')
  h.review.dispose()
})

test('single item recheck updates only its own row and preserves other decisions on failure', async () => {
  const h = harness()
  const trigger = h.review.openTrigger(source)
  h.triggers[0]!.resolve(status('completed', '7')); await trigger
  const load = h.review.loadCollection()
  h.collections[0]!.resolve(draftCollection([draftItem(0), draftItem(1)])); await load
  const recheck = h.review.reloadItem(1)
  assert.equal(h.items[0]?.index, 1)
  h.items[0]!.resolve({ ...draftItem(1, 'succeeded'), task_id: '99' }); await recheck
  assert.equal(h.state.collection?.items[0]?.status, 'waiting_confirmation')
  assert.equal(h.state.collection?.items[1]?.task_id, '99')
  const failed = h.review.reloadItem(0)
  h.items[1]!.reject(new ApiError(503, 'internal')); await failed
  assert.equal(h.state.collection?.items[1]?.task_id, '99')
  assert.match(h.state.itemError, /稍后/)
  h.review.dispose()
})

test('old collection or item results never cross run, group or account changes', async () => {
  const h = harness()
  const trigger = h.review.openTrigger(source)
  h.triggers[0]!.resolve(status('completed', '7')); await trigger
  const oldLoad = h.review.loadCollection()
  h.collections[0]!.resolve(draftCollection([draftItem(0)], '8')); await oldLoad
  assert.equal(h.state.collection, null)
  const load = h.review.loadCollection()
  h.collections[1]!.resolve(draftCollection([draftItem(0)])); await load
  const oldItem = h.review.reloadItem(0)
  h.review.openTrigger({ ...source, messageId: '99' })
  h.items[0]!.resolve(draftItem(0, 'succeeded')); await oldItem
  assert.equal(h.state.collection, null)
  h.identity.setSession('two')
  assert.equal(h.state.mode, 'closed')
  h.review.dispose()
})

test('a changed run releases old collection loading without clearing the new load', async () => {
  const h = harness()
  const first = h.review.openTrigger(source)
  h.triggers[0]!.resolve(status('completed', '7')); await first
  const oldLoad = h.review.loadCollection()
  const refresh = h.review.refreshTrigger()
  h.triggers[1]!.resolve(status('completed', '9')); await refresh
  const newLoad = h.review.loadCollection()
  assert.equal(h.collections.length, 2)
  h.collections[0]!.resolve(draftCollection([draftItem(0)], '7')); await oldLoad
  assert.equal(h.state.collectionBusy, true)
  h.collections[1]!.resolve(draftCollection([draftItem(0, 'succeeded')], '9')); await newLoad
  assert.equal(h.state.collection?.run_id, '9')
  assert.equal(h.state.collectionBusy, false)
  h.review.dispose()
})

async function loadedReview(h: ReturnType<typeof harness>, drafts = [draftItem(0), draftItem(1)]) {
  const trigger = h.review.openTrigger(source)
  h.triggers.at(-1)!.resolve(status('completed', '7')); await trigger
  const load = h.review.loadCollection()
  h.collections.at(-1)!.resolve(draftCollection(drafts)); await load
}

test('unsaved text blocks item switching until explicit discard or successful save', async () => {
  const h = harness(); await loadedReview(h)
  h.review.updateText('  新标题  ', '  新说明  ')
  assert.equal(h.review.hasUnsavedText(), true)
  assert.equal(h.review.selectItem(1), false)
  assert.equal(h.state.selectedIndex, 0)
  const save = h.review.editText(0)
  assert.deepEqual(h.writes[0]?.body, { title: '新标题', description: '新说明', expected_revision: '1' })
  h.writes[0]!.resolve({ ...draftItem(0), draft: { ...draftItem(0).draft, title: '新标题', description: '新说明', revision: '2' } }); await save
  assert.equal(h.review.hasUnsavedText(), false)
  assert.equal(h.review.selectItem(1), true)
  h.review.updateText('草稿', '')
  assert.equal(h.review.selectItem(0, true), true)
  assert.equal(h.state.textInput?.title, '新标题')
  h.review.dispose()
})

test('409 keeps local text, rereads only the conflicted item, and uses its new revision on next save', async () => {
  const h = harness(); await loadedReview(h)
  h.review.updateText('我的修改', '')
  const save = h.review.editText(0)
  h.writes[0]!.reject(new ApiError(409, 'revision conflict'))
  await Promise.resolve(); await Promise.resolve()
  assert.equal(h.items.length, 1)
  h.items[0]!.resolve({ ...draftItem(0), draft: { ...draftItem(0).draft, revision: '3', title: '他人修改' } }); await save
  assert.equal(h.state.textInput?.title, '我的修改')
  assert.equal(h.state.collection?.items[0]?.draft.title, '他人修改')
  assert.equal(h.state.collection?.items[1]?.draft.revision, '1')
  assert.match(h.state.writeError, /重新核对/)
  const again = h.review.editText(0)
  assert.deepEqual(h.writes[1]?.body, { title: '我的修改', description: '', expected_revision: '3' })
  h.writes[1]!.resolve({ ...draftItem(0), draft: { ...draftItem(0).draft, title: '我的修改', revision: '4' } }); await again
  assert.equal(h.review.hasUnsavedText(), false)
  h.review.dispose()
})

test('assignee and deadline choices send current revision and replace only authoritative saved item', async () => {
  const h = harness()
  await loadedReview(h, [draftItem(0), draftItem(1)])
  const assignee = h.review.selectAssignee(0, '0')
  assert.deepEqual(h.writes[0]?.body, { assignee_id: '0', expected_revision: '1' })
  h.writes[0]!.resolve({ ...draftItem(0), draft: { ...draftItem(0).draft, assignee_resolution: 'unassigned', revision: '2' } }); await assignee
  const deadline = h.review.editDeadline(0, 0)
  assert.deepEqual(h.writes[1]?.body, { due_at_unix_ms: 0, expected_revision: '2' })
  h.writes[1]!.resolve({ ...draftItem(0), draft: { ...draftItem(0).draft, assignee_resolution: 'unassigned', deadline: { ...draftItem(0).draft.deadline, resolution: 'unset' }, revision: '3' } }); await deadline
  assert.equal(h.state.collection?.items[0]?.draft.deadline.resolution, 'unset')
  assert.equal(h.state.collection?.items[1]?.draft.revision, '1')
  h.review.dispose()
})

test('uncertain edit rereads same item and old write result cannot cross account change', async () => {
  const h = harness(); await loadedReview(h)
  const pending = h.review.selectAssignee(0, '8')
  h.writes[0]!.reject(new ApiError(504, 'timeout'))
  await Promise.resolve(); await Promise.resolve()
  h.items[0]!.resolve({ ...draftItem(0), draft: { ...draftItem(0).draft, revision: '2', assignee_id: '8', assignee_resolution: 'selected' } }); await pending
  assert.equal(h.state.collection?.items[0]?.draft.assignee_id, '8')
  const old = h.review.editDeadline(0, 0)
  h.identity.setSession('two')
  h.writes[1]!.resolve(draftItem(0)); await old
  assert.equal(h.state.collection, null)
  h.review.dispose()
})

test('collection refresh preserves the selected item and unsaved text for the same run', async () => {
  const h = harness(); await loadedReview(h)
  h.review.selectItem(1)
  h.review.updateText('稍后保存', '')
  const refresh = h.review.loadCollection()
  h.collections[1]!.resolve(draftCollection([draftItem(0), draftItem(1)])); await refresh
  assert.equal(h.state.selectedIndex, 1)
  assert.equal(h.state.textInput?.title, '稍后保存')
  assert.equal(h.review.hasUnsavedText(), true)
  h.review.dispose()
})

test('confirm sends all saved fields once and separates created task from reply status', async () => {
  const h = harness(); await loadedReview(h)
  h.review.updateText('未保存', '')
  await h.review.confirm(0)
  assert.equal(h.decisions.length, 0)
  h.review.updateText('任务', '')
  const pending = h.review.confirm(0)
  assert.deepEqual(h.decisions[0]?.body, { expected_title: '任务', expected_description: '', expected_revision: '1', expected_assignee_id: '0', expected_due_at_unix_ms: 0, expected_deadline_resolution: 'none' })
  await h.review.skip(1)
  assert.equal(h.decisions.length, 1)
  h.decisions[0]!.resolve({ ...draftItem(0, 'succeeded'), reply_status: 'not_started' }); await pending
  assert.equal(h.state.collection?.items[0]?.task_id, '12')
  assert.equal(h.state.collection?.items[0]?.reply_status, 'not_started')
  assert.deepEqual(h.state.counts, { waiting: 1, created: 1, skipped: 0 })
  h.review.dispose()
})

test('unresolved assignee or deadline blocks confirm while skip uses current revision', async () => {
  const h = harness()
  const unresolved = { ...draftItem(0), draft: { ...draftItem(0).draft, assignee_resolution: 'ambiguous' as const, assignee_name: '小张', deadline: { ...draftItem(0).draft.deadline, resolution: 'needs_input' as const } } }
  await loadedReview(h, [unresolved, draftItem(1)])
  await h.review.confirm(0)
  assert.equal(h.decisions.length, 0)
  const skipped = h.review.skip(0)
  assert.deepEqual(h.decisions[0]?.body, { expected_revision: '1' })
  h.decisions[0]!.resolve({ ...unresolved, status: 'skipped', reply_status: 'disabled' }); await skipped
  assert.equal(h.state.counts.skipped, 1)
  await h.review.confirm(0)
  assert.equal(h.decisions.length, 1)
  h.review.dispose()
})

test('409 and uncertain confirm or skip reread only original item and never automatically repeat writes', async () => {
  const h = harness(); await loadedReview(h)
  const confirm = h.review.confirm(0)
  h.decisions[0]!.reject(new ApiError(409, 'conflict'))
  await Promise.resolve(); await Promise.resolve()
  assert.equal(h.items[0]?.index, 0)
  h.items[0]!.resolve({ ...draftItem(0), draft: { ...draftItem(0).draft, revision: '2' } }); await confirm
  assert.equal(h.decisions.length, 1)
  assert.equal(h.state.collection?.items[0]?.draft.revision, '2')
  const skip = h.review.skip(1)
  h.decisions[1]!.reject(new ApiError(504, 'timeout'))
  await Promise.resolve(); await Promise.resolve()
  assert.equal(h.items[1]?.index, 1)
  h.items[1]!.resolve({ ...draftItem(1), status: 'skipped' }); await skip
  assert.equal(h.decisions.length, 2)
  assert.equal(h.state.collection?.items[1]?.status, 'skipped')
  assert.match(h.state.writeError, /核对/)
  h.review.dispose()
})

test('creating is frozen until same item recheck, then explicit retry uses stored values', async () => {
  const h = harness(); await loadedReview(h, [draftItem(0, 'creating')])
  await h.review.editText(0)
  await h.review.skip(0)
  await h.review.confirm(0)
  assert.equal(h.decisions.length, 0)
  const recheck = h.review.reloadItem(0)
  h.items[0]!.resolve(draftItem(0, 'creating')); await recheck
  const retry = h.review.confirm(0)
  assert.equal(h.decisions[0]?.kind, 'confirm')
  h.decisions[0]!.resolve(draftItem(0, 'succeeded')); await retry
  assert.equal(h.state.collection?.items[0]?.status, 'succeeded')
  h.review.dispose()
})

test('reply retry requires reread succeeded item and never retries unknown or accepted', async () => {
  const h = harness(); await loadedReview(h, [{ ...draftItem(0, 'succeeded'), reply_status: 'not_started' }])
  await h.review.retryReply(0)
  assert.equal(h.decisions.length, 0)
  const check = h.review.reloadItem(0)
  h.items[0]!.resolve({ ...draftItem(0, 'succeeded'), reply_status: 'pending', reply_msg_id: 'bot-task:7' }); await check
  const retry = h.review.retryReply(0)
  assert.equal(h.decisions[0]?.kind, 'reply')
  h.decisions[0]!.resolve({ ...draftItem(0, 'succeeded'), reply_status: 'accepted', reply_msg_id: 'bot-task:7' }); await retry
  await h.review.retryReply(0)
  assert.equal(h.decisions.length, 1)
  const unknownCheck = h.review.reloadItem(0)
  h.items[1]!.resolve({ ...draftItem(0, 'succeeded'), reply_status: 'unknown' }); await unknownCheck
  await h.review.retryReply(0)
  assert.equal(h.decisions.length, 1)
  h.review.dispose()
})

test('client abort rechecks the same decision and old result cannot cross identity', async () => {
  const h = harness(); await loadedReview(h)
  const skip = h.review.skip(0)
  h.decisions[0]!.reject(new DOMException('abort', 'AbortError'))
  await Promise.resolve(); await Promise.resolve()
  h.items[0]!.resolve(draftItem(0)); await skip
  assert.equal(h.decisions.length, 1)
  const old = h.review.confirm(0)
  h.identity.setSession('two')
  h.decisions[1]!.resolve(draftItem(0, 'succeeded')); await old
  assert.equal(h.state.collection, null)
  h.review.dispose()
})

test('ordinary collection and item reads cannot race an in-flight decision', async () => {
  const h = harness(); await loadedReview(h)
  const pending = h.review.confirm(0)
  void h.review.loadCollection()
  void h.review.reloadItem(0)
  assert.equal(h.collections.length, 1)
  assert.equal(h.items.length, 0)
  h.decisions[0]!.resolve(draftItem(0, 'succeeded')); await pending
  assert.equal(h.state.collection?.items[0]?.status, 'succeeded')
  const loading = h.review.loadCollection()
  void h.review.skip(1)
  assert.equal(h.decisions.length, 1)
  h.collections[1]!.resolve(draftCollection([draftItem(0, 'succeeded'), draftItem(1)])); await loading
  h.review.dispose()
})
