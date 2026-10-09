import test from 'node:test'
import assert from 'node:assert/strict'
import { ApiError } from '../api/client.ts'
import { createSession } from '../auth/session.ts'
import { createMemberDirectory, createTaskCreator, initialCreateState, initialMemberDirectoryState, normalizeCreate, randomIdempotencyKey, shanghaiDateTimeToUnixMs } from './create.ts'

test('Shanghai datetime conversion is timezone independent and validates calendar and bounds', () => {
  assert.equal(shanghaiDateTimeToUnixMs(''), 0)
  assert.equal(shanghaiDateTimeToUnixMs('2026-10-09T08:30'), Date.UTC(2026, 9, 9, 0, 30))
  assert.equal(shanghaiDateTimeToUnixMs('1970-01-01T08:01'), 60_000)
  for (const value of ['2026-02-29T12:00', '2026-13-01T00:00', '0000-01-01T00:00', '0001-01-01T00:00', '1970-01-01T08:00', '10000-01-01T00:00']) assert.throws(() => shanghaiDateTimeToUnixMs(value))
  assert.match(randomIdempotencyKey(), /^[A-Za-z0-9._~-]{1,64}$/)
})

test('create fields normalize text, ids, source pairs and limits', () => {
  assert.deepEqual(normalizeCreate({ teamId: '2', title: '  发布清单  ', description: '  逐项核对  ', assigneeId: '0', sourceGroupId: '7', sourceMessageId: '8', dueLocal: '' }), {
    teamId: '2', body: { title: '发布清单', description: '逐项核对', assignee_id: '0', source_group_id: '7', source_message_id: '8', due_at_unix_ms: 0 },
  })
  assert.throws(() => normalizeCreate({ teamId: '2', title: ' ', description: '', assigneeId: '0', sourceGroupId: '7', sourceMessageId: '0', dueLocal: '' }))
  assert.throws(() => normalizeCreate({ teamId: '2', title: 'a'.repeat(201), description: '', assigneeId: 'x', sourceGroupId: '0', sourceMessageId: '0', dueLocal: '' }))
})

test('uncertain create replays the frozen key and body, then detail failure only rechecks detail', async () => {
  const identity = createSession(), state = initialCreateState(); identity.setSession('a')
  const posts: { key: string; body: string }[] = []; let postCalls = 0, details = 0
  const request = async (path: string, options?: RequestInit) => {
    if (options?.method === 'POST') {
      posts.push({ key: new Headers(options.headers).get('Idempotency-Key')!, body: String(options.body) })
      if (++postCalls === 1) throw new ApiError(504, 'timeout')
      return { task_id: '9' }
    }
    assert.equal(path, '/teams/2/tasks/9')
    if (++details === 1) throw new ApiError(503, 'detail down')
    return { task: { task_id: '9', team_id: '2', team_name: '研发', title: '标题', description: '', creator_id: '3', creator_name: '甲', assignee_id: '0', assignee_name: '', status: 0, source_group_id: '0', source_message_id: '0', due_at_unix_ms: '0' }, can_update_status: true }
  }
  const creator = createTaskCreator(request, identity, state, () => 'fixed_key')
  const form = { teamId: '2', title: ' 标题 ', description: '', assigneeId: '0', sourceGroupId: '0', sourceMessageId: '0', dueLocal: '' }
  await creator.submit(form)
  assert.equal(state.phase, 'uncertain'); assert.equal(state.frozen, true)
  await creator.retry()
  assert.equal(state.phase, 'detail-uncertain'); assert.equal(posts.length, 2); assert.deepEqual(posts[0], posts[1])
  await creator.retry()
  assert.equal(state.phase, 'success'); assert.equal(posts.length, 2)
  creator.dispose()
})

test('503 after a committed create keeps the original key and body for recovery', async () => {
  const identity = createSession(); identity.setSession('a')
  const calls: { key: string; body: string }[] = []
  const creator = createTaskCreator(async (path, options) => {
    if (options?.method === 'POST') {
      calls.push({ key: new Headers(options.headers).get('Idempotency-Key')!, body: String(options.body) })
      if (calls.length === 1) throw new ApiError(503, 'temporarily unavailable after commit')
      return { task_id: '9' }
    }
    assert.equal(path, '/teams/2/tasks/9')
    return { task: { task_id: '9', team_id: '2', team_name: '研发', title: '标题', description: '', creator_id: '3', creator_name: '甲', assignee_id: '0', assignee_name: '', status: 0, source_group_id: '0', source_message_id: '0', due_at_unix_ms: '0' }, can_update_status: true }
  }, identity, initialCreateState(), () => 'fixed_key')
  await creator.submit({ teamId: '2', title: '标题', description: '', assigneeId: '0', sourceGroupId: '0', sourceMessageId: '0', dueLocal: '' })
  assert.equal(creator.state.phase, 'uncertain'); assert.equal(creator.state.frozen, true)
  await creator.retry()
  assert.equal(creator.state.phase, 'success'); assert.deepEqual(calls, [calls[0], calls[0]])
  creator.dispose()
})

test('definite create failure unlocks and uses task-specific conflict text; stale responses are ignored', async () => {
  const identity = createSession(); identity.setSession('a')
  const state = initialCreateState(); let reject!: (e: unknown) => void
  const creator = createTaskCreator(() => new Promise((_resolve, no) => { reject = no }), identity, state, () => 'valid_key')
  const pending = creator.submit({ teamId: '2', title: '标题', description: '', assigneeId: '0', sourceGroupId: '0', sourceMessageId: '0', dueLocal: '' })
  identity.setSession('b'); reject(new ApiError(409, '用户名已存在')); await pending
  assert.equal(state.phase, 'idle'); assert.equal(state.error, '')
  creator.dispose()

  const secondState = initialCreateState()
  const second = createTaskCreator(async () => { throw new ApiError(409, '用户名已存在') }, createSession(), secondState, () => 'valid_key')
  await second.submit({ teamId: '2', title: '标题', description: '', assigneeId: '0', sourceGroupId: '0', sourceMessageId: '0', dueLocal: '' })
  assert.equal(secondState.frozen, false); assert.match(secondState.error, /请求键/); assert.doesNotMatch(secondState.error, /用户名/)
  second.dispose()
})

test('member team switch starts the new request immediately and rejects the old response', async () => {
  const deferred = () => { let resolve!: (value: unknown) => void; const promise = new Promise<unknown>(done => { resolve = done }); return { promise, resolve } }
  const a = deferred(), b = deferred(), calls: string[] = []
  const state = initialMemberDirectoryState()
  const directory = createMemberDirectory(async path => { calls.push(path); return path.includes('/teams/2/') ? a.promise : b.promise }, state)
  let assigneeId = '9'
  const first = directory.select('2')
  assigneeId = '0'
  const second = directory.select('3')
  assert.equal(assigneeId, '0'); assert.equal(state.loading, true); assert.equal(calls.length, 2)
  a.resolve({ members: [{ user_id: '20', username: 'old', nickname: '旧成员' }], next_after_user_id: '0' }); await first
  assert.equal(state.loading, true); assert.equal(state.items.length, 0)
  b.resolve({ members: [{ user_id: '30', username: 'new', nickname: '新成员' }], next_after_user_id: '0' }); await second
  assert.equal(state.loading, false); assert.deepEqual(state.items.map(item => item.user_id), ['30'])
  directory.dispose()
})
