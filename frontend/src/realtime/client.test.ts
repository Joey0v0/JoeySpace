import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createRealtimeClient, type SocketLike, type SendStatus, type TextChat } from './client.ts'

function fixture() {
  let token: string | null = 'jwt-one'
  let version = 1
  const listeners = new Set<() => void>()
  const sockets: FakeSocket[] = []
  const ticketRequests: { auth: string | null; signal?: AbortSignal }[] = []
  const statuses: SendStatus[] = []
  const chats: TextChat[] = []
  const states: string[] = []
  let refreshes = 0
  let ticketNumber = 0
  const identity = {
    token: () => token,
    version: () => version,
    subscribe(fn: () => void) { listeners.add(fn); return () => { listeners.delete(fn) } },
    clearSession() { changeToken(null) },
  }
  function changeToken(next: string | null) {
    token = next
    version++
    for (const listener of listeners) listener()
  }
  const transport: typeof fetch = async (_url, init) => {
    const headers = new Headers(init?.headers)
    ticketRequests.push({ auth: headers.get('Authorization'), signal: init?.signal ?? undefined })
    return new Response(JSON.stringify({ code: 0, data: { ticket: 'a'.repeat(42) + ++ticketNumber } }), { status: 200 })
  }
  const client = createRealtimeClient({
    identity, transport, origin: 'https://chat.example.test',
    socketFactory: url => { const socket = new FakeSocket(url); sockets.push(socket); return socket },
    onState: state => states.push(state),
    onChat: chat => chats.push(chat),
    onSendStatus: status => statuses.push(status),
    onRefresh: () => { refreshes++ },
  })
  return { client, sockets, ticketRequests, statuses, chats, states, changeToken, refreshes: () => refreshes }
}

class FakeSocket implements SocketLike {
  readonly url: string
  readyState = 0
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  sent: string[] = []
  constructor(url: string) { this.url = url }
  open() { this.readyState = 1; this.onopen?.(new Event('open')) }
  receive(frame: unknown) { this.onmessage?.({ data: JSON.stringify(frame) } as MessageEvent) }
  close() { this.readyState = 3; this.onclose?.(new Event('close') as CloseEvent) }
  send(data: string) { if (this.readyState !== 1) throw new Error('closed'); this.sent.push(data) }
}

async function tick() { await new Promise(resolve => setTimeout(resolve, 0)) }
const exampleChat = (msgId = 'm-1') => ({
  type: 'chat', data: { id: '9007199254740993', msg_id: msgId,
    from_id: '9007199254740995', to_id: '9007199254740997', sender_type: 1,
    initiator_id: '0', chat_type: 2, content_type: 1, content: '你好',
    created_at: '2026-10-09T12:00:00Z' },
})

test('uses a one-time ticket URL and never places the JWT in the socket URL', async () => {
  const f = fixture()
  f.client.connect()
  await tick()
  assert.equal(f.ticketRequests[0].auth, 'Bearer jwt-one')
  assert.match(f.sockets[0].url, /^wss:\/\/chat\.example\.test\/ws\?ticket=/)
  assert.doesNotMatch(f.sockets[0].url, /jwt-one/)
  f.sockets[0].open()
  assert.equal(f.client.state(), 'connected')
  assert.equal(f.refreshes(), 1)
  f.client.dispose()
})

test('one in-flight send, ACK is only accepted, and server error is uncertain', async () => {
  const f = fixture()
  f.client.connect(); await tick(); f.sockets[0].open()
  const input = { msgId: 'm-1', toId: '9007199254740993', chatType: 2 as const, content: '你好' }
  assert.equal(f.client.send(input), true)
  assert.equal(f.client.send({ ...input, msgId: 'm-2' }), false)
  assert.deepEqual(JSON.parse(f.sockets[0].sent[0]), { type: 'chat', data: {
    msg_id: 'm-1', to_id: input.toId, chat_type: 2, content_type: 1, content: '你好',
  } })
  f.sockets[0].receive({ type: 'ack', data: { msg_id: 'wrong' } })
  assert.equal(f.client.send({ ...input, msgId: 'm-2' }), false)
  f.sockets[0].receive({ type: 'ack', data: { msg_id: 'm-1' } })
  assert.deepEqual(f.statuses.slice(0, 2).map(value => value.status), ['sending', 'accepted'])
  assert.equal(f.client.send({ ...input, msgId: 'm-2' }), true)
  f.sockets[0].receive({ type: 'error', data: { code: 403, msg: 'denied' } })
  assert.deepEqual(f.statuses.at(-1), { msgId: 'm-2', status: 'uncertain', error: '发送结果待核对' })
  f.client.confirmPersisted('m-1')
  assert.equal(f.statuses.at(-1)?.status, 'confirmed')
  f.client.dispose()
})

test('oversized UTF-8 frame is rejected before socket send', async () => {
  const f = fixture()
  f.client.connect(); await tick(); f.sockets[0].open()
  assert.equal(f.client.send({ msgId: 'm-large', toId: '1', chatType: 1, content: '中'.repeat(2000) }), false)
  assert.equal(f.sockets[0].sent.length, 0)
  f.client.dispose()
})

test('structured mentions stay exact string IDs across send and incoming chat', async () => {
  const f = fixture()
  f.client.connect(); await tick(); f.sockets[0].open()
  const ids = ['9007199254740993', '9007199254740995']
  assert.equal(f.client.send({ msgId: 'mention-1', toId: '8', chatType: 2, content: '@同事 请看', mentionedUserIds: ids }), true)
  assert.deepEqual((JSON.parse(f.sockets[0].sent[0]) as { data: { mentioned_user_ids: string[] } }).data.mentioned_user_ids, ids)
  f.sockets[0].receive({ ...exampleChat('mention-in'), data: { ...exampleChat('mention-in').data, mentioned_user_ids: ids } })
  assert.deepEqual(f.chats[0]?.mentionedUserIds, ids)
  f.sockets[0].receive({ ...exampleChat('bad-mention'), data: { ...exampleChat('bad-mention').data, mentioned_user_ids: [ids[0], ids[0]] } })
  assert.equal(f.chats.length, 1)
  f.client.dispose()
})

test('persisted chat before ACK releases pending send and ignores late ACK', async () => {
  const f = fixture()
  f.client.connect(); await tick(); f.sockets[0].open()
  const first = { msgId: 'early-history', toId: '2', chatType: 1 as const, content: 'first' }
  assert.equal(f.client.send(first), true)
  f.client.confirmPersisted(first.msgId)
  assert.deepEqual(f.statuses.at(-1), { msgId: first.msgId, status: 'confirmed' })
  assert.equal(f.client.send({ ...first, msgId: 'second' }), true)
  f.sockets[0].receive({ type: 'ack', data: { msg_id: first.msgId } })
  assert.deepEqual(f.statuses.at(-1), { msgId: 'second', status: 'sending' })
  f.sockets[0].receive({ type: 'ack', data: { msg_id: 'second' } })
  f.client.dispose()
})

test('rejects malformed and non-text chat frames and deduplicates by msg_id', async () => {
  const f = fixture()
  f.client.connect(); await tick(); f.sockets[0].open()
  f.sockets[0].receive(exampleChat())
  f.sockets[0].receive(exampleChat())
  f.sockets[0].receive({ ...exampleChat('m-2'), data: { ...exampleChat('m-2').data, id: 9007199254740993 } })
  f.sockets[0].receive({ ...exampleChat('m-3'), data: { ...exampleChat('m-3').data, content_type: 4 } })
  f.sockets[0].receive({ ...exampleChat('m-4'), data: { ...exampleChat('m-4').data, from_id: '01' } })
  assert.equal(f.chats.length, 1)
  assert.equal(f.chats[0].id, '9007199254740993')
  assert.equal(f.chats[0].content, '你好')
  f.client.dispose()
})

test('identity change closes old socket and ignores stale events; new ticket refreshes', async () => {
  const f = fixture()
  f.client.connect(); await tick(); f.sockets[0].open()
  const staleMessage = f.sockets[0].onmessage
  f.changeToken('jwt-two')
  await tick()
  assert.equal(f.sockets[0].readyState, 3)
  staleMessage?.({ data: JSON.stringify(exampleChat('old')) } as MessageEvent)
  assert.equal(f.chats.length, 0)
  assert.equal(f.ticketRequests[1].auth, 'Bearer jwt-two')
  f.sockets[1].open()
  assert.equal(f.refreshes(), 2)
  f.sockets[1].receive(exampleChat('old'))
  assert.equal(f.chats.length, 1)
  f.changeToken(null)
  assert.equal(f.sockets[1].readyState, 3)
  assert.equal(f.client.state(), 'disconnected')
  f.client.dispose()
})

test('disconnect leaves pending send uncertain without retry or changed message ID', async () => {
  const f = fixture()
  f.client.connect(); await tick(); f.sockets[0].open()
  f.client.send({ msgId: 'same-id', toId: '2', chatType: 1, content: 'hello' })
  f.client.disconnect()
  assert.deepEqual(f.statuses.at(-1), {
    msgId: 'same-id', status: 'uncertain', error: '连接已关闭，发送结果待核对',
  })
  assert.equal(f.sockets[0].sent.length, 1)
  f.client.dispose()
})

test('unexpected close reconnects with a fresh ticket and requests authoritative refresh', async () => {
  const f = fixture()
  f.client.connect(); await tick(); f.sockets[0].open()
  f.sockets[0].receive(exampleChat())
  f.sockets[0].close()
  assert.equal(f.client.state(), 'reconnecting')
  await new Promise(resolve => setTimeout(resolve, 550))
  assert.equal(f.ticketRequests.length, 2)
  assert.notEqual(f.sockets[0].url, f.sockets[1].url)
  f.sockets[1].open()
  assert.equal(f.refreshes(), 2)
  f.sockets[1].receive(exampleChat())
  assert.equal(f.chats.length, 1)
  f.client.dispose()
})

test('stale ticket response cannot open a socket after logout', async () => {
  let resolveTicket!: (response: Response) => void
  const request = new Promise<Response>(resolve => { resolveTicket = resolve })
  const listeners = new Set<() => void>()
  let token: string | null = 'jwt'
  let epoch = 1
  let opened = 0
  const client = createRealtimeClient({
    identity: {
      token: () => token, version: () => epoch,
      subscribe(listener) { listeners.add(listener); return () => { listeners.delete(listener) } },
    },
    transport: async () => request,
    origin: 'https://chat.example.test',
    socketFactory: () => { opened++; return new FakeSocket('unused') },
  })
  client.connect()
  token = null; epoch++
  for (const listener of listeners) listener()
  resolveTicket(new Response(JSON.stringify({ code: 0, data: { ticket: 'a'.repeat(43) } })))
  await tick()
  assert.equal(opened, 0)
  assert.equal(client.state(), 'disconnected')
  client.dispose()
})
