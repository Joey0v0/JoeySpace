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
  let now = 0
  let nextTimer = 0
  const timers = new Map<number, { due: number; callback: () => void }>()
  const api = {
    ask: () => new Promise<{ answer: string }>((resolve, reject) => { asks.push({ resolve, reject }) }),
    trigger: (scope: TriggerScope) => new Promise<AgentTrigger>((resolve, reject) => { triggers.push({ scope, resolve, reject }) }),
    collection: () => new Promise<DraftCollection>((resolve, reject) => { collections.push({ resolve, reject }) }),
    item: (_scope: unknown, index: number) => new Promise<DraftItem>((resolve, reject) => { items.push({ index, resolve, reject }) }),
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
  return { identity, state, asks, triggers, collections, items, timers, review, advance }
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
