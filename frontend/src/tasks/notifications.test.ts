import assert from 'node:assert/strict'
import test from 'node:test'
import { ApiError } from '../api/client.ts'
import { createSession } from '../auth/session.ts'
import { taskSignal } from '../realtime/taskSignal.ts'
import { createTaskNotifications, decodeNotificationPage, initialNotificationState, type TaskNotification } from './notifications.ts'

const item = (id = '9007199254740993'): TaskNotification => ({ notification_id: id, team_id: '11', team_name: '平台组', task_id: '22', task_title: '发布', actor_id: '33', actor_name: '小乔', from_status: 0, to_status: 1, current_status: 1, created_at_unix_ms: '1790874000123', read_at_unix_ms: '0' })

test('decodes notification pages without losing large decimal strings and rejects invalid rows', () => {
  assert.deepEqual(decodeNotificationPage({ notifications: [item()], next_cursor: 'eyJ2IjoxfQ', unread_count: '9007199254740993' }), { notifications: [item()], next_cursor: 'eyJ2IjoxfQ', unread_count: '9007199254740993' })
  assert.equal(decodeNotificationPage({ notifications: [{ ...item(), read_at_unix_ms: '1' }], next_cursor: '', unread_count: '1' }), null)
  assert.equal(decodeNotificationPage({ notifications: [item(), item()], next_cursor: '', unread_count: '2' }), null)
  assert.equal(decodeNotificationPage({ notifications: [{ ...item(), created_at_unix_ms: '253402300800000' }], next_cursor: '', unread_count: '1' }), null)
})
test('filtered zero page cannot clear a global hint and an unrelated team hint does not refresh', async () => {
  const identity = createSession({ getItem: () => null, setItem() {}, removeItem() {} }); identity.setSession('token')
  let calls = 0
  const state = initialNotificationState(); state.teamId = '7'
  const store = createTaskNotifications(async () => { calls++; return { notifications: [], next_cursor: '', unread_count: '0' } }, async () => ({ notification_id: '1', read_at_unix_ms: '1' }), identity, state)
  taskSignal.set(); await store.load()
  assert.equal(taskSignal.pending(), true)
  store.hint('90', '8'); await new Promise(resolve => setTimeout(resolve, 0))
  assert.equal(calls, 1)
  assert.equal(taskSignal.pending(), true)
  store.dispose(); taskSignal.clear()
})

test('dispose invalidates an in-flight page and activating a loaded tab consumes pending refresh', async () => {
  const identity = createSession({ getItem: () => null, setItem() {}, removeItem() {} }); identity.setSession('token')
  let resolve!: (value: ReturnType<typeof decodeNotificationPage>) => void
  const pending = new Promise<any>(done => { resolve = done })
  const state = initialNotificationState()
  const store = createTaskNotifications(async () => pending, async () => ({ notification_id: '1', read_at_unix_ms: '1' }), identity, state)
  taskSignal.clear(); const load = store.load(); store.dispose(); resolve({ notifications: [item()], next_cursor: '', unread_count: '1' }); await load
  assert.deepEqual(state.items, []); assert.equal(taskSignal.pending(), false)

  let calls = 0
  const activeState = initialNotificationState(); activeState.loaded = true
  const active = createTaskNotifications(async () => { calls++; return { notifications: [], next_cursor: '', unread_count: '0' } }, async () => ({ notification_id: '1', read_at_unix_ms: '1' }), identity, activeState)
  taskSignal.set(); active.activate(); await new Promise(done => setTimeout(done, 0))
  assert.equal(calls, 1); assert.equal(taskSignal.pending(), false)
  active.dispose()
})

test('403 read denial clears all private notification content and paging state', async () => {
  const identity = createSession({ getItem: () => null, setItem() {}, removeItem() {} }); identity.setSession('token')
  const state = initialNotificationState(); state.items = [item('9'), item('8')]; state.cursor = 'next'; state.unreadCount = '2'; state.loaded = true; state.teamId = '7'
  const store = createTaskNotifications(async () => ({ notifications: [], next_cursor: '', unread_count: '0' }), async () => { throw new ApiError(403, 'denied') }, identity, state)
  await store.markRead(state.items[0])
  assert.deepEqual(state.items, []); assert.equal(state.cursor, ''); assert.equal(state.unreadCount, '0'); assert.equal(state.teamId, '7'); assert.match(state.error, /denied/)
  store.dispose()
})

test('preserves a loaded page on refresh and continuation failures and deduplicates merged IDs', async () => {
  const identity = createSession({ getItem: () => null, setItem() {}, removeItem() {} })
  identity.setSession('token')
  let calls = 0
  const pages = [
    { notifications: [item('9')], next_cursor: 'next', unread_count: '1' },
    new ApiError(503, 'down'),
    { notifications: [item('9'), item('8')], next_cursor: '', unread_count: '1' },
  ]
  const state = initialNotificationState()
  const store = createTaskNotifications(async () => { const value = pages[calls++]; if (value instanceof Error) throw value; return value }, async () => ({ notification_id: '9', read_at_unix_ms: '1790874000999' }), identity, state)
  await store.load()
  await store.refresh()
  assert.deepEqual(state.items.map(value => value.notification_id), ['9'])
  assert.equal(state.cursor, 'next')
  await store.loadMore()
  assert.deepEqual(state.items.map(value => value.notification_id), ['9', '8'])
  store.dispose()
})

test('queues only one authoritative refresh and read always refreshes from the first page', async () => {
  const identity = createSession({ getItem: () => null, setItem() {}, removeItem() {} }); identity.setSession('token')
  const cursors: string[] = []
  let release!: () => void
  const gate = new Promise<void>(resolve => { release = resolve })
  let calls = 0
  const state = initialNotificationState()
  const store = createTaskNotifications(async options => { cursors.push(options.cursor); if (calls++ === 0) await gate; return { notifications: [item()], next_cursor: 'later', unread_count: '1' } }, async () => ({ notification_id: '9007199254740993', read_at_unix_ms: '1790874000999' }), identity, state)
  const first = store.load(); store.hint('9007199254740993'); store.hint('9007199254740993'); release(); await first; await new Promise(resolve => setTimeout(resolve, 0))
  assert.deepEqual(cursors, ['', ''])
  await store.markRead(state.items[0])
  assert.equal(cursors.at(-1), '')
  store.dispose()
})
