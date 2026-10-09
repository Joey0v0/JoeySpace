import test from 'node:test'
import assert from 'node:assert/strict'
import { ApiError } from '../api/client.ts'
import { createSession } from '../auth/session.ts'
import type { Selection } from './directory.ts'
import { createHistory } from './history.ts'

const direct: Selection = { key: 'direct:9007199254740995', kind: 'direct', title: '同事' }
const group: Selection = { key: 'group:3:4', kind: 'group', title: '讨论', teamId: '3', groupId: '4', joined: true }
const received = (id: string) => ({ id, msg_id: 'm' + id, from_id: '9007199254740995', to_id: '9007199254740993', content_type: 1, content: '你好', created_at_unix_ms: 1000 })
const own = (id: string) => ({ ...received(id), from_id: '9007199254740993', to_id: '9007199254740995' })
const tick = () => new Promise(resolve => setImmediate(resolve))

test('direct latest and older pages preserve large IDs, deduplicate, and keep cursor on failure', async () => {
  let olderFails = false
  const paths: string[] = []
  const history = createHistory(async path => {
    paths.push(path)
    if (path.endsWith('/unread')) return { peer_id: '9007199254740995', unread_count: '2' }
    if (path.includes('before_message_id=9007199254740994')) {
      if (olderFails) throw new ApiError(503, '暂时不可用')
      return { messages: [received('9007199254740993')], next_before_message_id: '0' }
    }
    return { messages: [own('9007199254740996'), received('9007199254740994')], next_before_message_id: '9007199254740994' }
  }, createSession(), '9007199254740993')
  history.select(direct); await tick()
  assert.deepEqual(history.state.messages.map(item => item.id), ['9007199254740994', '9007199254740996'])
  assert.equal(history.state.unreadCount, '2')
  olderFails = true; await history.loadOlder()
  assert.equal(history.state.cursor, '9007199254740994')
  assert.equal(history.state.messages.length, 2)
  olderFails = false; await history.loadOlder()
  assert.deepEqual(history.state.messages.map(item => item.id), ['9007199254740993', '9007199254740994', '9007199254740996'])
  assert.equal(history.state.cursor, '0')
  assert.ok(paths.some(path => path.includes('before_message_id=9007199254740994')))
  history.dispose()
})

test('explicit read submits only loaded received IDs and never reads on opening history', async () => {
  const calls: { path: string; body?: string }[] = []
  const history = createHistory(async (path, options) => {
    calls.push({ path, body: options?.body as string | undefined })
    if (path.endsWith('/unread')) return { peer_id: '9007199254740995', unread_count: calls.some(call => call.path.endsWith('/read')) ? '0' : '2' }
    if (path.endsWith('/read')) return { peer_id: '9007199254740995', unread_count: '0', message_ids: ['1'] }
    return { messages: [received('1'), own('2'), received('3')], next_before_message_id: '0' }
  }, createSession(), '9007199254740993')
  history.select(direct); await tick()
  assert.equal(calls.filter(call => call.path.endsWith('/read')).length, 0)
  assert.deepEqual(history.receivedIDs(), ['1', '3'])
  await history.markLoadedRead()
  assert.deepEqual(JSON.parse(calls.find(call => call.path.endsWith('/read'))!.body!), { message_ids: ['1', '3'] })
  assert.equal(history.state.unreadCount, '0')
  history.dispose()
})

test('group read includes received bot messages and caps a batch at 100', async () => {
  let submitted: string[] = []
  const history = createHistory(async (path, options) => {
    if (path.endsWith('/unread')) return { team_id: '3', group_id: '4', unread_count: '101' }
    if (path.endsWith('/read')) { submitted = JSON.parse(options!.body as string).message_ids; return { team_id: '3', group_id: '4', unread_count: '1' } }
    return { messages: [], next_before_message_id: '0' }
  }, createSession(), '9007199254740993')
  history.select(group); await tick()
  history.state.messages = Array.from({ length: 101 }, (_, index) => ({ ...received(String(index + 1)), sender_type: index === 0 ? 2 : 1 }))
  history.state.messages.push({ ...own('102'), sender_type: 1 })
  await history.markLoadedRead()
  assert.equal(submitted.length, 100)
  assert.equal(submitted[0], '1')
  assert.equal(submitted.includes('102'), false)
  history.dispose()
})

test('timeout requires unread recheck before a read retry', async () => {
  let fail = true
  const history = createHistory(async (path) => {
    if (path.endsWith('/unread')) return { peer_id: '9007199254740995', unread_count: '1' }
    if (path.endsWith('/read')) { if (fail) throw new ApiError(504, '超时'); return { peer_id: '9007199254740995', unread_count: '0' } }
    if (path.includes('before_message_id=0')) return { messages: [received('1')], next_before_message_id: '0' }
    throw new ApiError(403, '撤权')
  }, createSession(), '9007199254740993')
  history.select(direct); await tick()
  await history.markLoadedRead()
  assert.equal(history.state.pendingConfirmation, true)
  assert.match(history.state.markError, /待核对/)
  await history.refreshUnread()
  assert.equal(history.state.pendingConfirmation, false)
  fail = false; await history.markLoadedRead()
  assert.equal(history.state.unreadCount, '1')
  history.dispose()
})

test('403 on history clears current private messages', async () => {
  let denied = false
  const history = createHistory(async path => {
    if (path.endsWith('/unread')) return { team_id: '3', group_id: '4', unread_count: '1' }
    if (denied) throw new ApiError(403, '撤权')
    return { messages: [received('1')], next_before_message_id: '1' }
  }, createSession(), '9007199254740993')
  history.select(group); await tick()
  assert.equal(history.state.messages.length, 1)
  denied = true; await history.loadOlder()
  assert.equal(history.state.denied, true)
  assert.deepEqual(history.state.messages, [])
  assert.deepEqual(history.receivedIDs(), [])
  history.dispose()
})

test('old response cannot reappear after conversation or account changes', async () => {
  const pending: ((value: unknown) => void)[] = []
  const identity = createSession()
  const history = createHistory(path => path.endsWith('/unread') ? Promise.resolve({ peer_id: '9007199254740995', unread_count: '1' }) : new Promise(resolve => pending.push(resolve)), identity, '9007199254740993')
  history.select(direct)
  identity.setSession('new')
  pending[0]!({ messages: [received('1')], next_before_message_id: '0' })
  await tick()
  assert.deepEqual(history.state.messages, [])
  assert.equal(history.state.loaded, false)
  history.dispose()
})
