import test from 'node:test'
import assert from 'node:assert/strict'
import { createSession } from '../auth/session.ts'
import { createOfflineInbox, type OfflineMessage } from './offline.ts'

const sample = (id: string): OfflineMessage => ({
  id, msg_id: 'msg-' + id, from_id: '9007199254740995', to_id: '9007199254740993',
  sender_type: 1, initiator_id: '0', chat_type: 1, content_type: 1,
  content: '你好', created_at: '2026-10-09T12:00:00Z',
})

test('offline delivery enters identity cache before ACK and does not mark messages read', async () => {
  const identity = createSession(); identity.setSession('token')
  const calls: string[] = []
  const cached: OfflineMessage[] = []
  const inbox = createOfflineInbox(async (path, options) => {
    calls.push(path)
    if (path.endsWith('/ack')) {
      assert.deepEqual(JSON.parse(options!.body as string), { message_ids: ['9007199254740993'] })
      assert.equal(cached.length, 1)
      return undefined
    }
    return [sample('9007199254740993')]
  }, identity, items => { cached.push(...items) })
  await inbox.pull()
  assert.deepEqual(calls, ['/message/offline', '/message/offline/ack'])
  assert.equal(inbox.state.error, '')
  inbox.dispose()
})

test('malformed offline response is not acknowledged', async () => {
  const identity = createSession(); identity.setSession('token')
  let acked = false
  const inbox = createOfflineInbox(async path => {
    if (path.endsWith('/ack')) { acked = true; return undefined }
    return [{ ...sample('1'), to_id: 9007199254740993 }]
  }, identity, () => {})
  await inbox.pull()
  assert.equal(acked, false)
  assert.match(inbox.state.error, /无效/)
  inbox.dispose()
})

test('account switch before pull resolves never caches or acknowledges old messages', async () => {
  const identity = createSession(); identity.setSession('first')
  let resolve!: (value: unknown) => void
  const response = new Promise<unknown>(ready => { resolve = ready })
  let cached = 0, acked = 0
  const inbox = createOfflineInbox(path => { if (path.endsWith('/ack')) acked++; return response }, identity, items => { cached += items.length })
  const pending = inbox.pull()
  identity.setSession('second')
  resolve([sample('1')])
  await pending
  assert.equal(cached, 0)
  assert.equal(acked, 0)
  inbox.dispose()
})

test('large accumulation only acknowledges the first thousand and keeps a pending hint', async () => {
  const identity = createSession(); identity.setSession('token')
  let ackCount = 0
  const inbox = createOfflineInbox(async (path, options) => {
    if (path.endsWith('/ack')) { ackCount = JSON.parse(options!.body as string).message_ids.length; return undefined }
    return Array.from({ length: 1001 }, (_, index) => sample(String(index + 1)))
  }, identity, () => {})
  await inbox.pull()
  assert.equal(ackCount, 1000)
  assert.equal(inbox.state.morePending, true)
  inbox.dispose()
})
