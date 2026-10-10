import test from 'node:test'
import assert from 'node:assert/strict'
import { ApiError } from '../api/client.ts'
import { createSession } from '../auth/session.ts'
import { createStatusMutation, initialStatusMutationState } from './mutations.ts'
import type { TaskDetail } from './model.ts'

const detail = (status: 0 | 1 | 2): TaskDetail => ({ task: { task_id: '9', team_id: '2', team_name: '研发', title: '任务', description: '', creator_id: '3', creator_name: '甲', assignee_id: '4', assignee_name: '乙', status, source_group_id: '0', source_message_id: '0', due_at_unix_ms: '0' }, can_update_status: true })

test('status submits expected status and rereads detail and list after success', async () => {
  const bodies: unknown[] = [], applied: number[] = []; let refreshed = 0
  const mutation = createStatusMutation(async (_p, options, authenticated, expectData) => { assert.equal(authenticated, true); assert.equal(expectData, false); bodies.push(JSON.parse(String(options?.body))); return undefined }, async () => detail(1), createSession(), initialStatusMutationState(), { apply: value => applied.push(value.task.status), clear: () => {}, refresh: async () => { refreshed++ } })
  await mutation.update(detail(0), 1)
  assert.deepEqual(bodies, [{ status: 1, expected_status: 0 }]); assert.deepEqual(applied, [1]); assert.equal(refreshed, 1)
  mutation.dispose()
})

test('timeout recheck distinguishes applied, retryable old state, conflict, and failed recheck', async () => {
  for (const [server, phase] of [[1, 'success'], [0, 'retryable'], [2, 'conflict']] as const) {
    const state = initialStatusMutationState(); let applied = -1
    const mutation = createStatusMutation(async () => { throw new ApiError(504, 'timeout') }, async () => detail(server), createSession(), state, { apply: value => { applied = value.task.status }, clear: () => {}, refresh: async () => {} })
    await mutation.update(detail(0), 1)
    assert.equal(state.phase, phase); assert.equal(applied, server)
    mutation.dispose()
  }
  const state = initialStatusMutationState(); let puts = 0
  const mutation = createStatusMutation(async () => { puts++; throw new ApiError(0, 'network') }, async () => { throw new ApiError(503, 'down') }, createSession(), state, { apply: () => {}, clear: () => {}, refresh: async () => {} })
  await mutation.update(detail(0), 1); assert.equal(state.phase, 'uncertain'); await mutation.recheck(); assert.equal(puts, 1)
  mutation.dispose()
})

test('409 uses server state and task conflict text; denied recheck clears detail', async () => {
  const state = initialStatusMutationState(); let cleared = 0
  let denied = false
  const mutation = createStatusMutation(async () => { throw new ApiError(409, '用户名已存在') }, async () => denied ? Promise.reject(new ApiError(403, 'denied')) : detail(2), createSession(), state, { apply: () => {}, clear: () => { cleared++ }, refresh: async () => {} })
  await mutation.update(detail(0), 1)
  assert.equal(state.phase, 'conflict'); assert.match(state.error, /任务状态已变化/); assert.doesNotMatch(state.error, /用户名/)
  denied = true; await mutation.recheck(); assert.equal(cleared, 1)
  mutation.dispose()
})
