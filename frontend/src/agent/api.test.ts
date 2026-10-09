import test from 'node:test'
import assert from 'node:assert/strict'
import { createAgentApi } from './api.ts'
import { ApiError, createApiClient, StaleRequestError } from '../api/client.ts'
import { createSession } from '../auth/session.ts'
const scope = { teamId: '9007199254740993', groupId: '9223372036854775807', runId: '9007199254740995' }
const revision = '9007199254741001'
const draft = { revision, title: '任务', description: '', assignee_id: '0', assignee_name: '', assignee_resolution: 'none', due_at_unix_ms: 0, source_message_id: '0', deadline: { text: '', source: 'none', source_message_id: '0', reference_unix_ms: 0, timezone: 'Asia/Shanghai', resolution: 'none', reason: '', parsed_unix_ms: 0, instruction_reference_unix_ms: 0 } }
const item = { item_index: 0, status: 'waiting_confirmation', task_id: '0', reply_status: 'not_started', reply_msg_id: '', draft }
const single = { run_id: scope.runId, team_id: scope.teamId, group_id: scope.groupId, item_count: 1, item }
const collection = { ...single, items: [item] }
const triggerScope = { teamId: scope.teamId, groupId: scope.groupId, messageId: '9007199254741011' }
const trigger = { team_id: scope.teamId, group_id: scope.groupId, message_id: triggerScope.messageId, status: 'completed', run_id: scope.runId }
const confirm = { expected_title: '任务', expected_description: '', expected_revision: revision, expected_assignee_id: '9007199254740997', expected_due_at_unix_ms: 1791097200123, expected_deadline_resolution: 'selected' as const }
const invalid = (error: unknown) => error instanceof ApiError && error.status === 400
const malformed = (error: unknown) => error instanceof ApiError && error.status === 502 && !error.message.includes('secret')

test('Agent operations send exact routes, verbs, bodies and operation timeouts', async () => {
  const calls: unknown[] = []
  const api = createAgentApi(async <T>(path: string, options: RequestInit = {}, auth?: boolean, data?: boolean, timeout?: number): Promise<T> => {
    calls.push([path, options.method, options.body === undefined ? undefined : JSON.parse(String(options.body)), auth, data, timeout])
    return (path.endsWith('/ask') ? { answer: '回答' } : path.includes('/agent-triggers/') ? trigger : path.endsWith('/drafts') ? collection : single) as T
  })
  assert.deepEqual(await api.ask(scope.teamId, scope.groupId, '\u0085😀问题\u3000'), { answer: '回答' })
  assert.equal((await api.trigger(triggerScope)).run_id, scope.runId)
  assert.equal((await api.collection(scope)).items[0]?.draft.revision, revision)
  assert.equal((await api.item(scope, 0)).draft.revision, revision)
  await api.editText(scope, 0, { title: ' 任务 ', description: ' ', expected_revision: revision })
  await api.selectAssignee(scope, 0, { assignee_id: '9007199254740997', expected_revision: revision })
  await api.editDeadline(scope, 0, { due_at_unix_ms: 1791097200123, expected_revision: revision })
  await api.confirm(scope, 0, confirm)
  await api.skip(scope, 0, { expected_revision: revision })
  await api.retryReply(scope, 0)
  assert.deepEqual(calls, [
    ['/teams/9007199254740993/groups/9223372036854775807/ask', 'POST', { question: '😀问题' }, true, true, 25000],
    ['/teams/9007199254740993/groups/9223372036854775807/agent-triggers/9007199254741011', 'GET', undefined, true, true, 18000],
    ['/agent/runs/9007199254740995/drafts', 'GET', undefined, true, true, 18000],
    ['/agent/runs/9007199254740995/drafts/0', 'GET', undefined, true, true, 18000],
    ['/agent/runs/9007199254740995/drafts/0', 'PUT', { title: '任务', description: '', expected_revision: revision }, true, true, 18000],
    ['/agent/runs/9007199254740995/drafts/0/assignee', 'PUT', { assignee_id: '9007199254740997', expected_revision: revision }, true, true, 18000],
    ['/agent/runs/9007199254740995/drafts/0/deadline', 'PUT', { due_at_unix_ms: 1791097200123, expected_revision: revision }, true, true, 18000],
    ['/agent/runs/9007199254740995/drafts/0/confirm', 'POST', confirm, true, true, 23000],
    ['/agent/runs/9007199254740995/drafts/0/skip', 'POST', { expected_revision: revision }, true, true, 18000],
    ['/agent/runs/9007199254740995/drafts/0/reply/retry', 'POST', undefined, true, true, 22000],
  ])
})
test('invalid paths, Unicode input and write preconditions reject before HTTP', async () => {
  let count = 0
  const api = createAgentApi(async <T>(): Promise<T> => { count++; return single as T })
  for (const id of ['0', '01', '-1', '1/2', '9223372036854775808', '']) {
    await assert.rejects(api.ask(id, scope.groupId, '问题'), invalid)
    await assert.rejects(api.trigger({ ...triggerScope, messageId: id }), invalid)
    await assert.rejects(api.collection({ ...scope, teamId: id }), invalid)
    await assert.rejects(api.item({ ...scope, runId: id }, 0), invalid)
  }
  for (const index of [-1, 5, 0.5, NaN]) await assert.rejects(api.retryReply(scope, index), invalid)
  for (const question of ['', ' \u0085 ', '😀'.repeat(2001), '\ud800']) await assert.rejects(api.ask(scope.teamId, scope.groupId, question), invalid)
  await assert.rejects(api.editText(scope, 0, { title: ' ', description: '', expected_revision: revision }), invalid)
  await assert.rejects(api.selectAssignee(scope, 0, { assignee_id: '01', expected_revision: revision }), invalid)
  await assert.rejects(api.skip(scope, 0, { expected_revision: '0' }), invalid)
  await assert.rejects(api.editDeadline(scope, 0, { due_at_unix_ms: -1, expected_revision: revision }), invalid)
  await assert.rejects(api.confirm(scope, 0, { ...confirm, expected_title: ' 任务' }), invalid)
  await assert.rejects(api.confirm(scope, 0, { ...confirm, expected_due_at_unix_ms: '0' as unknown as number }), invalid)
  assert.equal(count, 0)
})
test('all Agent response reads and writes reject malformed or mismatched scope safely', async () => {
  for (const raw of [null, { secret: 'private' }, { ...single, team_id: '2' }, { ...single, group_id: '2' }, { ...single, run_id: '2' }, { ...single, item: { ...item, item_index: 1 } }]) {
    const api = createAgentApi(async <T>(): Promise<T> => raw as T)
    for (const operation of [() => api.item(scope, 0), () => api.editText(scope, 0, { title: '任务', description: '', expected_revision: revision }), () => api.selectAssignee(scope, 0, { assignee_id: '0', expected_revision: revision }), () => api.editDeadline(scope, 0, { due_at_unix_ms: 0, expected_revision: revision }), () => api.confirm(scope, 0, confirm), () => api.skip(scope, 0, { expected_revision: revision }), () => api.retryReply(scope, 0)]) await assert.rejects(operation(), malformed)
    await assert.rejects(api.collection(scope), malformed)
    await assert.rejects(api.trigger(triggerScope), malformed)
    await assert.rejects(api.ask(scope.teamId, scope.groupId, '问题'), malformed)
  }
  for (const field of ['team_id', 'group_id', 'run_id']) {
    await assert.rejects(createAgentApi(async <T>() => ({ ...collection, [field]: '2' }) as T).collection(scope), malformed)
  }
  for (const field of ['team_id', 'group_id', 'message_id']) {
    await assert.rejects(createAgentApi(async <T>() => ({ ...trigger, [field]: '2' }) as T).trigger(triggerScope), malformed)
  }
  for (const raw of [{ answer: '' }, { answer: ' ' }, { answer: 1 }, { answer: '回答', secret: 'private' }]) await assert.rejects(createAgentApi(async <T>() => raw as T).ask(scope.teamId, scope.groupId, '问题'), malformed)
})
test('Agent requests propagate identity switching and do not automatically retry failures', async () => {
  const identity = createSession(); identity.setSession('old')
  let complete!: (response: Response) => void
  let count = 0
  const client = createApiClient(identity, () => { count++; return new Promise(resolve => { complete = resolve }) })
  const pending = createAgentApi(client.request).confirm(scope, 0, confirm)
  identity.setSession('new')
  complete(new Response(JSON.stringify({ code: 0, data: single })))
  await assert.rejects(pending, StaleRequestError)
  assert.equal(count, 1)
  const fail = createAgentApi(async () => { count++; throw new ApiError(503, '暂不可用') })
  await assert.rejects(fail.retryReply(scope, 0), ApiError)
  assert.equal(count, 2)
})
