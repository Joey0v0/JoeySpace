import test from 'node:test'
import assert from 'node:assert/strict'
import { ApiError } from '../api/client.ts'
import { createSession } from '../auth/session.ts'
import { canCreateTaskFromMessage, createSourceContext, decodeSourceContext, initialSourceContextState, sourceFocusRoute, taskFromMessageRoute } from './sourceContext.ts'

const message = (id: string, overrides: Record<string, unknown> = {}) => ({ id, msg_id: 'm-' + id, from_id: '3', sender_type: 1, initiator_id: '0', content_type: 1, content: '正文', created_at_unix_ms: '1791500000000', mentioned_user_ids: [], ...overrides })
test('source context strictly validates order, exact target, cap and large ids', () => {
  const large = '9007199254740993'
  assert.equal(decodeSourceContext({ messages: [message(large)], target_message_id: large }, large)?.target_message_id, large)
  assert.equal(decodeSourceContext({ messages: [message('2'), message('1')], target_message_id: '1' }, '1'), null)
  assert.equal(decodeSourceContext({ messages: [message('1'), message('1')], target_message_id: '1' }, '1'), null)
  assert.equal(decodeSourceContext({ messages: Array.from({ length: 42 }, (_, i) => message(String(i + 1))), target_message_id: '21' }, '21'), null)
  assert.equal(decodeSourceContext({ messages: [message('1', { sender_type: 2, initiator_id: '0' })], target_message_id: '1' }, '1'), null)
})

test('source context accepts omitted empty mentions from the real IM response', () => {
  const { mentioned_user_ids: _unused, ...withoutMentions } = message('8')
  assert.deepEqual(decodeSourceContext({ messages: [withoutMentions], target_message_id: '8' }, '8')?.messages[0]?.mentioned_user_ids, [])
})

test('context clears on denied, route clear and account change, with stale response isolation', async () => {
  const identity = createSession(); identity.setSession('a'); const state = initialSourceContextState()
  let resolve!: (value: unknown) => void
  const source = createSourceContext(() => new Promise(done => { resolve = done }), identity, state)
  const pending = source.load('2', '7', '8'); source.clear(); resolve({ messages: [message('8')], target_message_id: '8' }); await pending
  assert.equal(state.context, null)
  source.dispose()
  const deniedState = initialSourceContextState()
  const denied = createSourceContext(async () => { throw new ApiError(403, 'denied') }, identity, deniedState)
  await denied.load('2', '7', '8'); assert.equal(deniedState.context, null)
  identity.setSession('b'); assert.equal(deniedState.error, '')
  denied.dispose()
})

test('only persisted group messages produce exact source and focus routes', () => {
  assert.equal(canCreateTaskFromMessage('group', message('8')), true)
  assert.equal(canCreateTaskFromMessage('direct', message('8')), false)
  assert.equal(canCreateTaskFromMessage('group', { ...message('8'), id: '' }), false)
  assert.equal(taskFromMessageRoute('2', '7', '8'), '/tasks/new?team_id=2&source_group_id=7&source_message_id=8')
  assert.equal(sourceFocusRoute('2', '7', '8'), '/messages/teams/2/groups/7?focus_message_id=8')
})
