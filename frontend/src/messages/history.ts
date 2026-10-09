import { ApiError, StaleRequestError, errorText, isId } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'
import type { Selection } from './directory.ts'

export interface ChatMessage {
  id: string
  msg_id: string
  from_id: string
  to_id?: string
  sender_type?: number
  initiator_id?: string
  content_type: number
  content: string
  created_at_unix_ms: number
}
type Request = (path: string, options?: RequestInit) => Promise<any>
export function initialHistoryState() {
  return { messages: [] as ChatMessage[], cursor: '0', loaded: false, loading: false, error: '', unreadCount: '' as string, unreadLoading: false, unreadError: '', marking: false, markError: '', pendingConfirmation: false, denied: false }
}
type State = ReturnType<typeof initialHistoryState>
const decimal = (value: unknown) => value === '0' || isId(value)
const count = (value: unknown) => typeof value === 'string' && /^\d+$/.test(value)
const message = (item: any): item is ChatMessage => item && isId(item.id) && typeof item.msg_id === 'string' && isId(item.from_id) && (item.to_id === undefined || isId(item.to_id)) && (item.sender_type === undefined || Number.isInteger(item.sender_type)) && (item.initiator_id === undefined || decimal(item.initiator_id)) && Number.isInteger(item.content_type) && typeof item.content === 'string' && Number.isSafeInteger(item.created_at_unix_ms) && item.created_at_unix_ms >= 0 && item.created_at_unix_ms <= 8640000000000000
const newestFirst = (a: ChatMessage, b: ChatMessage) => BigInt(a.id) > BigInt(b.id) ? -1 : BigInt(a.id) < BigInt(b.id) ? 1 : 0
export function conversationPath(conversation: Selection) {
  return conversation.kind === 'group' ? '/teams/' + conversation.teamId + '/groups/' + conversation.groupId : '/direct/' + conversation.key.slice('direct:'.length)
}
export function createHistory(request: Request, identity: ReturnType<typeof createSession>, ownId: string, state: State = initialHistoryState()) {
  let selected: Selection | null = null
  let scope = 0
  let unreadRequest = 0
  let confirmedIDs = new Set<string>()
  let pendingBatch: string[] | null = null
  function clearReadBatch() { confirmedIDs = new Set(); pendingBatch = null }
  const unsubscribe = identity.subscribe(() => { scope++; selected = null; clearReadBatch(); Object.assign(state, initialHistoryState()) })
  function deny() { scope++; selected = null; clearReadBatch(); Object.assign(state, { ...initialHistoryState(), denied: true, error: '当前账号已无权访问这个会话' }) }
  function select(conversation: Selection | null) {
    scope++
    selected = conversation && (conversation.kind !== 'group' || conversation.joined) ? conversation : null
    clearReadBatch()
    Object.assign(state, initialHistoryState())
    if (selected) { void loadLatest(); void refreshUnread() }
  }
  async function loadLatest() {
    if (!selected || state.loading) return
    const selectedKey = selected.key, epoch = scope, path = conversationPath(selected)
    state.loading = true; state.error = ''
    try {
      const data = await request(path + '/messages?before_message_id=0&limit=30')
      if (epoch !== scope || selected?.key !== selectedKey) return
      if (!data || !Array.isArray(data.messages) || data.messages.length > 30 || !data.messages.every(message) || !decimal(data.next_before_message_id) || (!data.messages.length && data.next_before_message_id !== '0')) throw new ApiError(502, '消息数据无效，请重试')
      const items = data.messages as ChatMessage[]
      state.messages = [...new Map(items.map(item => [item.id, item])).values()].sort(newestFirst).reverse()
      state.cursor = data.next_before_message_id; state.loaded = true
    } catch (error) { if (epoch === scope && !(error instanceof StaleRequestError)) { if (error instanceof ApiError && (error.status === 403 || error.status === 404)) deny(); else state.error = errorText(error) } }
    finally { if (epoch === scope) state.loading = false }
  }
  async function loadOlder() {
    if (!selected || !state.loaded || state.loading || state.cursor === '0') return
    const selectedKey = selected.key, epoch = scope, path = conversationPath(selected), before = state.cursor
    state.loading = true; state.error = ''
    try {
      const data = await request(path + '/messages?before_message_id=' + before + '&limit=30')
      if (epoch !== scope || selected?.key !== selectedKey) return
      if (!data || !Array.isArray(data.messages) || data.messages.length > 30 || !data.messages.every(message) || data.messages.some((item: ChatMessage) => BigInt(item.id) >= BigInt(before)) || !decimal(data.next_before_message_id) || (!data.messages.length && data.next_before_message_id !== '0') || (data.next_before_message_id !== '0' && BigInt(data.next_before_message_id) >= BigInt(before))) throw new ApiError(502, '消息分页数据无效，请重试')
      const items = data.messages as ChatMessage[]
      state.messages = [...new Map([...state.messages, ...items].map(item => [item.id, item])).values()].sort(newestFirst).reverse()
      state.cursor = data.next_before_message_id
    } catch (error) { if (epoch === scope && !(error instanceof StaleRequestError)) { if (error instanceof ApiError && (error.status === 403 || error.status === 404)) deny(); else state.error = errorText(error) } }
    finally { if (epoch === scope) state.loading = false }
  }
  async function refreshUnread(force = false) {
    if (!selected || (state.unreadLoading && !force)) return
    const selectedKey = selected.key, epoch = scope, path = conversationPath(selected), requestNumber = ++unreadRequest
    state.unreadLoading = true; state.unreadError = ''
    try {
      const data = await request(path + '/unread')
      if (epoch !== scope || requestNumber !== unreadRequest || selected?.key !== selectedKey) return
      if (!data || !count(data.unread_count) || (selected.kind === 'group' && (data.team_id !== selected.teamId || data.group_id !== selected.groupId)) || (selected.kind === 'direct' && data.peer_id !== selected.key.slice('direct:'.length))) throw new ApiError(502, '未读数据无效，请重试')
      state.unreadCount = data.unread_count; state.pendingConfirmation = false; state.markError = ''
    } catch (error) { if (epoch === scope && requestNumber === unreadRequest && !(error instanceof StaleRequestError)) { if (error instanceof ApiError && (error.status === 403 || error.status === 404)) deny(); else state.unreadError = errorText(error) } }
    finally { if (epoch === scope && requestNumber === unreadRequest) state.unreadLoading = false }
  }
  function receivedIDs() {
    if (!selected || !isId(ownId)) return []
    return [...new Set(state.messages.filter(item => !confirmedIDs.has(item.id) && (selected?.kind === 'direct' ? item.from_id === selected.key.slice('direct:'.length) && item.to_id === ownId : item.sender_type === 2 || item.from_id !== ownId)).map(item => item.id))]
  }
  async function markLoadedRead() {
    if (!selected || state.marking || state.pendingConfirmation) return
    const ids = pendingBatch ?? receivedIDs().slice(0, 100)
    if (!ids.length) return
    const epoch = scope, selectedKey = selected.key, path = conversationPath(selected)
    state.marking = true; state.markError = ''
    try {
      await request(path + '/read', { method: 'POST', body: JSON.stringify({ message_ids: ids }) })
      if (epoch !== scope || selected?.key !== selectedKey) return
      ids.forEach(id => confirmedIDs.add(id))
      pendingBatch = null
      state.pendingConfirmation = false
      await refreshUnread(true)
    } catch (error) {
      if (epoch !== scope || error instanceof StaleRequestError) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) deny()
      else { pendingBatch = ids; state.pendingConfirmation = true; state.markError = errorText(error) + '；结果待核对，请刷新未读后再试' }
    } finally { if (epoch === scope) state.marking = false }
  }
  function dispose() { scope++; selected = null; unsubscribe() }
  return { state, select, loadLatest, loadOlder, refreshUnread, receivedIDs, markLoadedRead, dispose }
}
