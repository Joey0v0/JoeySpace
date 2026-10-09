import test from 'node:test'
import assert from 'node:assert/strict'
import { createSession } from '../auth/session.ts'
import { ApiError } from '../api/client.ts'
import type { createAgentApi, TriggerScope } from './api.ts'
import type { AgentTrigger } from './model.ts'
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
  let now = 0
  let nextTimer = 0
  const timers = new Map<number, { due: number; callback: () => void }>()
  const api = {
    ask: () => new Promise<{ answer: string }>((resolve, reject) => { asks.push({ resolve, reject }) }),
    trigger: (scope: TriggerScope) => new Promise<AgentTrigger>((resolve, reject) => { triggers.push({ scope, resolve, reject }) }),
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
  return { identity, state, asks, triggers, timers, review, advance }
}

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
