import test from 'node:test'
import assert from 'node:assert/strict'
import { createSession } from '../auth/session.ts'
import { createUnreadOverview, initialUnreadOverviewState, unreadConversationPath, type UnreadConversation } from './unreadOverview.ts'

const direct = (id: string, peerId = '9007199254740993'): UnreadConversation => ({
  chat_type: 1, team_id: '0', group_id: '0', group_name: '', peer_id: peerId,
  display_name: '同事', last_message_id: id, last_message_time_unix_ms: 1791500000000,
  preview: '待确认的消息', unread_count: '2', mention_unread_count: '0',
})
const group = (id: string): UnreadConversation => ({
  chat_type: 2, team_id: '33', group_id: '44', group_name: '项目讨论', peer_id: '0', display_name: '',
  last_message_id: id, last_message_time_unix_ms: 1791500000000,
  preview: '请检查最新方案', unread_count: '3', mention_unread_count: '0',
})

test('unread overview follows snapshot cursor, keeps large IDs, and refreshes from first page', async () => {
  const identity = createSession(); identity.setSession('first')
  const paths: string[] = []
  const first = { conversations: [direct('9007199254740999')], snapshot_upper_message_id: '9007199254741000', next_before_last_message_id: '9007199254740999' }
  const second = { conversations: [group('9007199254740998')], snapshot_upper_message_id: first.snapshot_upper_message_id, next_before_last_message_id: '0' }
  let call = 0
  const state = initialUnreadOverviewState()
  const overview = createUnreadOverview(async path => { paths.push(path); return ++call === 2 ? second : first }, identity, state)
  await overview.load()
  await overview.load()
  assert.deepEqual(state.items.map(unreadConversationPath), ['/messages/direct/9007199254740993', '/messages/teams/33/groups/44'])
  assert.equal(paths[1], '/messages/unread-conversations?snapshot_upper_message_id=9007199254741000&before_last_message_id=9007199254740999&limit=20')
  await overview.load()
  assert.equal(paths.length, 2)
  await overview.load(true)
  assert.equal(paths[2], paths[0])
  assert.deepEqual(state.items.map(item => item.chat_type), [1])
  overview.dispose()
})

test('failed continuation keeps loaded results and retry uses the same cursor', async () => {
  const identity = createSession(); identity.setSession('first')
  const paths: string[] = []
  const state = initialUnreadOverviewState()
  let call = 0
  const overview = createUnreadOverview(async path => {
    paths.push(path)
    if (++call === 2) throw Error('offline')
    return call === 1
      ? { conversations: [direct('9')], snapshot_upper_message_id: '10', next_before_last_message_id: '9' }
      : { conversations: [group('8')], snapshot_upper_message_id: '10', next_before_last_message_id: '0' }
  }, identity, state)
  await overview.load(); await overview.load()
  assert.equal(state.items.length, 1)
  assert.notEqual(state.error, '')
  await overview.retry()
  assert.equal(paths[1], paths[2])
  assert.equal(state.items.length, 2)
  overview.dispose()
})

test('old account and superseded refresh responses cannot enter new overview', async () => {
  const identity = createSession(); identity.setSession('first')
  let firstResolve!: (value: unknown) => void
  const first = new Promise<unknown>(resolve => { firstResolve = resolve })
  let call = 0
  const state = initialUnreadOverviewState()
  const overview = createUnreadOverview(() => ++call === 1 ? first : Promise.resolve({ conversations: [group('8')], snapshot_upper_message_id: '8', next_before_last_message_id: '0' }), identity, state)
  const old = overview.load()
  identity.setSession('second')
  await overview.load()
  firstResolve({ conversations: [direct('9')], snapshot_upper_message_id: '9', next_before_last_message_id: '0' })
  await old
  assert.deepEqual(state.items.map(item => item.chat_type), [2])
  overview.dispose()
})

test('malformed IDs, counts and failed refresh cannot overwrite a successful page', async () => {
  const identity = createSession(); identity.setSession('first')
  const state = initialUnreadOverviewState()
  let call = 0
  const overview = createUnreadOverview(async () => ++call === 1
    ? { conversations: [group('8')], snapshot_upper_message_id: '8', next_before_last_message_id: '0' }
    : { conversations: [{ ...direct('9'), unread_count: 2 }], snapshot_upper_message_id: '9', next_before_last_message_id: '0' }, identity, state)
  await overview.load(); await overview.load(true)
  assert.equal(state.items[0]?.group_id, '44')
  assert.match(state.error, /无效/)
  overview.dispose()
})
