import { ApiError, StaleRequestError, errorText, isId } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'

export interface UnreadConversation {
  chat_type: 1 | 2
  team_id: string
  group_id: string
  group_name: string
  peer_id: string
  display_name: string
  last_message_id: string
  last_message_time_unix_ms: number
  preview: string
  unread_count: string
  mention_unread_count: string
}
interface UnreadPage {
  conversations: UnreadConversation[]
  snapshot_upper_message_id: string
  next_before_last_message_id: string
}
export interface UnreadOverviewState {
  filter: 'all' | 'mentions'
  items: UnreadConversation[]
  snapshot: string
  cursor: string
  loaded: boolean
  loading: boolean
  error: string
}
type Request = (path: string) => Promise<unknown>
const count = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9]\d*)$/.test(value)
const cursor = (value: unknown): value is string => value === '0' || isId(value)
function invalid(): never { throw new ApiError(502, '未读总览数据无效，请重试') }

function validConversation(value: unknown): value is UnreadConversation {
  if (!value || typeof value !== 'object') return false
  const item = value as Partial<UnreadConversation>
  if ((item.chat_type !== 1 && item.chat_type !== 2) || !isId(item.last_message_id)
    || !Number.isSafeInteger(item.last_message_time_unix_ms) || item.last_message_time_unix_ms! < 0 || item.last_message_time_unix_ms! > 8_640_000_000_000_000
    || typeof item.preview !== 'string' || !count(item.unread_count) || !count(item.mention_unread_count)
    || BigInt(item.mention_unread_count) > BigInt(item.unread_count)) return false
  if (item.chat_type === 1) return isId(item.peer_id) && typeof item.display_name === 'string'
    && item.team_id === '0' && item.group_id === '0' && item.group_name === '' && item.mention_unread_count === '0'
  return isId(item.team_id) && isId(item.group_id) && typeof item.group_name === 'string'
    && item.peer_id === '0' && item.display_name === ''
}

function validPage(value: unknown): value is UnreadPage {
  if (!value || typeof value !== 'object') return false
  const page = value as Partial<UnreadPage>
  return Array.isArray(page.conversations) && page.conversations.every(validConversation)
    && cursor(page.snapshot_upper_message_id) && cursor(page.next_before_last_message_id)
}

export function initialUnreadOverviewState(): UnreadOverviewState {
  return { filter: 'all', items: [], snapshot: '0', cursor: '0', loaded: false, loading: false, error: '' }
}
export function conversationKey(item: UnreadConversation): string {
  return item.chat_type === 1 ? `direct:${item.peer_id}` : `group:${item.team_id}:${item.group_id}`
}
export function unreadConversationPath(item: UnreadConversation): string {
  return item.chat_type === 1 ? `/messages/direct/${item.peer_id}` : `/messages/teams/${item.team_id}/groups/${item.group_id}`
}

export function createUnreadOverview(request: Request, identity: ReturnType<typeof createSession>, state: UnreadOverviewState = initialUnreadOverviewState()) {
  let scope = 0
  let failedRefresh = false
  const unsubscribe = identity.subscribe(() => { scope++; Object.assign(state, initialUnreadOverviewState()) })
  async function load(refresh = false): Promise<void> {
    if (state.loading && !refresh) return
    if (!refresh && state.loaded && state.cursor === '0') return
    const ticket = ++scope
    failedRefresh = refresh
    const snapshot = refresh ? '0' : state.snapshot
    const before = refresh ? '0' : state.cursor
    state.loading = true
    state.error = ''
    try {
      const result = await request(`/messages/unread-conversations?snapshot_upper_message_id=${snapshot}&before_last_message_id=${before}&limit=20${state.filter === 'mentions' ? '&mentions_only=1' : ''}`)
      if (ticket !== scope) return
      if (!validPage(result)) invalid()
      if ((state.filter === 'mentions' && result.conversations.some(item => item.chat_type !== 2 || item.mention_unread_count === '0'))
        || (before !== '0' && result.snapshot_upper_message_id !== snapshot)
        || (before !== '0' && result.next_before_last_message_id !== '0' && BigInt(result.next_before_last_message_id) >= BigInt(before))) invalid()
      const existing = refresh ? [] : state.items
      const seen = new Set(existing.map(conversationKey))
      const rows = result.conversations.filter(item => { const key = conversationKey(item); if (seen.has(key)) return false; seen.add(key); return true })
      state.items = [...existing, ...rows]
      state.snapshot = result.snapshot_upper_message_id
      state.cursor = result.next_before_last_message_id
      state.loaded = true
    } catch (error) {
      if (ticket !== scope || error instanceof StaleRequestError) return
      state.error = errorText(error)
    } finally { if (ticket === scope) state.loading = false }
  }
  function setFilter(filter: 'all' | 'mentions') {
    if (state.filter === filter) return
    scope++
    Object.assign(state, initialUnreadOverviewState(), { filter })
    void load()
  }
  return { state, load, setFilter, retry: () => load(failedRefresh), dispose: () => { scope++; unsubscribe() } }
}
