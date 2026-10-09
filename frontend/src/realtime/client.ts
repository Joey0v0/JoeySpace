export type ConnectionState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'disconnected'
export type SendState = 'sending' | 'accepted' | 'confirmed' | 'uncertain'

export interface SendStatus { msgId: string; status: SendState; error?: string }
export interface TextChat {
  id: string
  msgId: string
  fromId: string
  toId: string
  senderType: 1 | 2
  initiatorId: string
  chatType: 1 | 2
  content: string
  createdAt: string
  mentionedUserIds?: string[]
}
export interface TextSend { msgId: string; toId: string; chatType: 1 | 2; content: string; mentionedUserIds?: string[] }
export interface RealtimeIdentity {
  token(): string | null
  version(): number
  subscribe(listener: () => void): () => void
  clearSession?(): void
}
export interface SocketLike {
  readonly readyState: number
  onopen: ((event: Event) => void) | null
  onmessage: ((event: MessageEvent) => void) | null
  onclose: ((event: CloseEvent) => void) | null
  onerror: ((event: Event) => void) | null
  send(data: string): void
  close(): void
}
export interface RealtimeOptions {
  identity: RealtimeIdentity
  transport?: typeof fetch
  socketFactory?: (url: string) => SocketLike
  origin?: string
  onState?: (state: ConnectionState) => void
  onChat?: (message: TextChat) => void
  onSendStatus?: (status: SendStatus) => void
  onRefresh?: () => void
}

const decimalId = (value: unknown, allowZero = false): value is string =>
  typeof value === 'string' && (allowZero ? /^(0|[1-9]\d*)$/.test(value) : /^[1-9]\d*$/.test(value)) &&
  BigInt(value) <= 9223372036854775807n
const msgIdValid = (value: unknown): value is string =>
  typeof value === 'string' && value.length > 0 && value.length <= 64 &&
  /^[!-~]+$/.test(value) && !value.toLowerCase().startsWith('bot-task:')
const mentionIdsValid = (value: unknown): value is string[] =>
  Array.isArray(value) && value.length <= 10 && value.every(item => decimalId(item)) && new Set(value).size === value.length

function parseTextChat(raw: unknown): TextChat | null {
  if (!raw || typeof raw !== 'object') return null
  const data = raw as Record<string, unknown>
  if (!decimalId(data.id) || !msgIdValid(data.msg_id) || !decimalId(data.from_id) ||
      !decimalId(data.to_id) || !decimalId(data.initiator_id, true) ||
      (data.sender_type !== 1 && data.sender_type !== 2) ||
      (data.chat_type !== 1 && data.chat_type !== 2) || data.content_type !== 1 ||
      typeof data.content !== 'string' || typeof data.created_at !== 'string' ||
      !Number.isFinite(Date.parse(data.created_at))) return null
  if (data.mentioned_user_ids !== undefined && (!mentionIdsValid(data.mentioned_user_ids) || data.chat_type !== 2 && data.mentioned_user_ids.length > 0)) return null
  return {
    id: data.id, msgId: data.msg_id, fromId: data.from_id, toId: data.to_id,
    senderType: data.sender_type, initiatorId: data.initiator_id,
    chatType: data.chat_type, content: data.content, createdAt: data.created_at,
    ...(data.mentioned_user_ids === undefined ? {} : { mentionedUserIds: data.mentioned_user_ids }),
  }
}

export function createRealtimeClient(options: RealtimeOptions) {
  const transport = options.transport ?? globalThis.fetch
  const socketFactory = options.socketFactory ?? ((url: string) => new WebSocket(url))
  let wanted = false
  let disposed = false
  let generation = 0
  let socket: SocketLike | null = null
  let ticketAbort: AbortController | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let ackTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectAttempts = 0
  let state: ConnectionState = 'idle'
  let pending: string | null = null
  const seen = new Set<string>()

  function setState(next: ConnectionState) {
    if (state !== next) { state = next; options.onState?.(next) }
  }
  function clearAckTimer() {
    if (ackTimer !== null) clearTimeout(ackTimer)
    ackTimer = null
  }
  function uncertain(reason: string) {
    if (pending !== null) options.onSendStatus?.({ msgId: pending, status: 'uncertain', error: reason })
    pending = null
    clearAckTimer()
  }
  function stopConnection(reason: string) {
    generation++
    ticketAbort?.abort()
    ticketAbort = null
    if (reconnectTimer !== null) clearTimeout(reconnectTimer)
    reconnectTimer = null
    uncertain(reason)
    const previous = socket
    socket = null
    if (previous) {
      previous.onopen = null
      previous.onmessage = null
      previous.onclose = null
      previous.onerror = null
      previous.close()
    }
  }
  function scheduleReconnect() {
    if (!wanted || disposed || !options.identity.token()) { setState('disconnected'); return }
    setState('reconnecting')
    const delay = Math.min(500 * (2 ** Math.min(reconnectAttempts++, 4)), 8000)
    reconnectTimer = setTimeout(() => { reconnectTimer = null; void openConnection() }, delay)
  }
  function handleFrame(frame: unknown) {
    if (!frame || typeof frame !== 'object') return
    const wire = frame as Record<string, unknown>
    if (wire.type === 'ack') {
      const data = wire.data as Record<string, unknown> | null
      if (data && msgIdValid(data.msg_id) && data.msg_id === pending) {
        clearAckTimer()
        pending = null
        options.onSendStatus?.({ msgId: data.msg_id, status: 'accepted' })
      }
    } else if (wire.type === 'error') {
      const data = wire.data as Record<string, unknown> | null
      if (data && typeof data.code === 'number' && Number.isInteger(data.code)) {
        // Server errors have no msg_id; only one unacknowledged send may exist.
        uncertain('发送结果待核对')
      }
    } else if (wire.type === 'chat') {
      const chat = parseTextChat(wire.data)
      if (!chat || seen.has(chat.msgId)) return
      seen.add(chat.msgId)
      if (seen.size > 1000) seen.delete(seen.values().next().value!)
      options.onChat?.(chat)
    }
  }
  async function openConnection() {
    if (!wanted || disposed || !options.identity.token()) return
    const currentGeneration = ++generation
    const identityVersion = options.identity.version()
    const token = options.identity.token()
    const abort = new AbortController()
    ticketAbort = abort
    setState(reconnectAttempts ? 'reconnecting' : 'connecting')
    const current = () => !disposed && wanted && generation === currentGeneration &&
      options.identity.version() === identityVersion && options.identity.token() === token
    try {
      const response = await transport('/ws-ticket', {
        method: 'POST', headers: { Authorization: `Bearer ${token}` }, signal: abort.signal,
      })
      if (!current()) return
      if (!response.ok) {
        if (response.status === 401) { wanted = false; options.identity.clearSession?.(); setState('disconnected'); return }
        throw new Error('ticket unavailable')
      }
      const envelope: unknown = await response.json()
      if (!current()) return
      const result = envelope as { code?: unknown; data?: { ticket?: unknown } } | null
      const ticket = result?.data?.ticket
      if (result?.code !== 0 || typeof ticket !== 'string' || !/^[A-Za-z0-9_-]{40,}$/.test(ticket)) throw new Error('invalid ticket')
      const origin = options.origin ?? globalThis.location?.origin
      if (!origin) throw new Error('missing origin')
      const url = new URL('/ws', origin)
      url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'
      url.searchParams.set('ticket', ticket)
      const candidate = socketFactory(url.toString())
      if (!current()) { candidate.close(); return }
      socket = candidate
      candidate.onopen = () => {
        if (!current() || socket !== candidate) return
        reconnectAttempts = 0
        setState('connected')
        options.onRefresh?.()
      }
      candidate.onmessage = event => {
        if (!current() || socket !== candidate || typeof event.data !== 'string') return
        try { handleFrame(JSON.parse(event.data) as unknown) } catch { /* malformed frame */ }
      }
      candidate.onclose = () => {
        if (!current() || socket !== candidate) return
        socket = null
        uncertain('连接已断开，发送结果待核对')
        scheduleReconnect()
      }
      candidate.onerror = () => {
        if (!current() || socket !== candidate) return
        candidate.close()
      }
    } catch {
      if (current()) scheduleReconnect()
    } finally {
      if (ticketAbort === abort) ticketAbort = null
    }
  }
  const unsubscribe = options.identity.subscribe(() => {
    if (disposed) return
    stopConnection('登录身份已改变，发送结果待核对')
    seen.clear()
    reconnectAttempts = 0
    if (wanted && options.identity.token()) void openConnection()
    else setState('disconnected')
  })
  function connect() {
    if (disposed || wanted) return
    wanted = true
    if (options.identity.token()) void openConnection()
    else setState('disconnected')
  }
  function disconnect() {
    wanted = false
    stopConnection('连接已关闭，发送结果待核对')
    setState('disconnected')
  }
  function dispose() {
    if (disposed) return
    disposed = true
    disconnect()
    unsubscribe()
    seen.clear()
  }
  function send(input: TextSend): boolean {
    if (disposed || state !== 'connected' || !socket || socket.readyState !== 1 || pending !== null ||
        !msgIdValid(input.msgId) || !decimalId(input.toId) ||
        (input.chatType !== 1 && input.chatType !== 2) ||
        typeof input.content !== 'string' || !input.content.trim() ||
        (input.mentionedUserIds !== undefined && (!mentionIdsValid(input.mentionedUserIds) || input.chatType !== 2 && input.mentionedUserIds.length > 0))) return false
    const frame = JSON.stringify({ type: 'chat', data: {
      msg_id: input.msgId, to_id: input.toId, chat_type: input.chatType,
      content_type: 1, content: input.content,
      ...(input.mentionedUserIds?.length ? { mentioned_user_ids: input.mentionedUserIds } : {}),
    } })
    if (new TextEncoder().encode(frame).length > 4096) return false
    pending = input.msgId
    options.onSendStatus?.({ msgId: input.msgId, status: 'sending' })
    try {
      socket.send(frame)
      ackTimer = setTimeout(() => uncertain('等待服务受理超时，发送结果待核对'), 10000)
      return true
    } catch {
      uncertain('连接异常，发送结果待核对')
      return false
    }
  }
  function confirmPersisted(msgId: string) {
    if (!msgIdValid(msgId)) return
    if (pending === msgId) { pending = null; clearAckTimer() }
    options.onSendStatus?.({ msgId, status: 'confirmed' })
  }
  function markUncertain(msgId: string) {
    if (msgIdValid(msgId)) options.onSendStatus?.({ msgId, status: 'uncertain', error: '持久记录待核对' })
  }
  return { connect, disconnect, dispose, send, confirmPersisted, markUncertain, state: () => state }
}
