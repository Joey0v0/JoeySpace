import { ApiError, StaleRequestError, errorText, isId } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'
import { taskSignal } from '../realtime/taskSignal.ts'

export interface TaskNotification {
  notification_id: string; team_id: string; team_name: string; task_id: string; task_title: string
  actor_id: string; actor_name: string; from_status: 0 | 1 | 2; to_status: 0 | 1 | 2
  current_status: 0 | 1 | 2; created_at_unix_ms: string; read_at_unix_ms: string
}
export interface NotificationPage { notifications: TaskNotification[]; next_cursor: string; unread_count: string }
type ListOptions = { teamId: string; cursor: string; limit: number }
type ReadResult = { notification_id: string; read_at_unix_ms: string }
const decimal = (value: unknown, positive = false): value is string => typeof value === 'string' &&
  (positive ? /^[1-9]\d*$/.test(value) : /^(0|[1-9]\d*)$/.test(value)) && BigInt(value) <= 9223372036854775807n
const cursor = (value: unknown): value is string => typeof value === 'string' && value.length <= 2048 && (value === '' || /^[A-Za-z0-9_-]+$/.test(value))
const maxTime = 253402300799999n
const status = (value: unknown): value is 0 | 1 | 2 => value === 0 || value === 1 || value === 2
function decodeNotification(value: unknown): TaskNotification | null {
  if (!value || typeof value !== 'object') return null
  const row = value as Record<string, unknown>
  if (!decimal(row.notification_id, true) || !decimal(row.team_id, true) || !decimal(row.task_id, true) || !decimal(row.actor_id, true) ||
      typeof row.team_name !== 'string' || typeof row.task_title !== 'string' || typeof row.actor_name !== 'string' ||
      !status(row.from_status) || !status(row.to_status) || row.from_status === row.to_status || !status(row.current_status) ||
      !decimal(row.created_at_unix_ms, true) || BigInt(row.created_at_unix_ms) > maxTime || !decimal(row.read_at_unix_ms) || BigInt(row.read_at_unix_ms) > maxTime ||
      (row.read_at_unix_ms !== '0' && BigInt(row.read_at_unix_ms) < BigInt(row.created_at_unix_ms))) return null
  return row as unknown as TaskNotification
}
export function decodeNotificationPage(value: unknown): NotificationPage | null {
  if (!value || typeof value !== 'object') return null
  const page = value as Record<string, unknown>
  if (!Array.isArray(page.notifications) || page.notifications.length > 50 || !cursor(page.next_cursor) || !decimal(page.unread_count)) return null
  const notifications: TaskNotification[] = []
  const seen = new Set<string>()
  for (const raw of page.notifications) {
    const item = decodeNotification(raw)
    if (!item || seen.has(item.notification_id)) return null
    seen.add(item.notification_id); notifications.push(item)
  }
  return { notifications, next_cursor: page.next_cursor, unread_count: page.unread_count }
}
export function initialNotificationState() {
  return { items: [] as TaskNotification[], cursor: '', unreadCount: '0', loaded: false, loading: false, error: '', teamId: '0', queuedRefresh: false, reading: '' }
}
type State = ReturnType<typeof initialNotificationState>
export function createTaskNotifications(list: (options: ListOptions) => Promise<NotificationPage>, read: (item: TaskNotification) => Promise<ReadResult>, identity: ReturnType<typeof createSession>, state: State = initialNotificationState()) {
  let scope = 0, refreshVersion = 0, disposed = false
  const unsubscribe = identity.subscribe(() => { scope++; refreshVersion++; Object.assign(state, initialNotificationState()); taskSignal.clear() })
  const merge = (old: TaskNotification[], next: TaskNotification[]) => [...new Map([...old, ...next].map(item => [item.notification_id, item])).values()]
  async function request(replace: boolean) {
    if (disposed) return
    if (state.loading) { if (replace) state.queuedRefresh = true; return }
    if (!replace && state.loaded && !state.cursor) return
    const ticket = scope, version = replace ? ++refreshVersion : refreshVersion, original = { items: state.items, cursor: state.cursor, unreadCount: state.unreadCount }
    state.loading = true; state.error = ''
    try {
      const result = await list({ teamId: state.teamId, cursor: replace ? '' : state.cursor, limit: 20 })
      if (disposed || ticket !== scope || version !== refreshVersion) return
      state.items = replace ? result.notifications : merge(state.items, result.notifications)
      state.cursor = result.next_cursor; state.unreadCount = result.unread_count; state.loaded = true
      if (BigInt(result.unread_count) > 0n) taskSignal.set(); else if (state.teamId === '0') taskSignal.clear()
    } catch (error) {
      if (disposed || ticket !== scope || error instanceof StaleRequestError) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) {
        const teamId = state.teamId
        scope++; refreshVersion++
        Object.assign(state, initialNotificationState(), { teamId, error: errorText(error) })
        return
      }
      state.items = original.items; state.cursor = original.cursor; state.unreadCount = original.unreadCount
      state.error = errorText(error); taskSignal.set()
    } finally {
      if (!disposed && ticket === scope) {
        state.loading = false
        if (state.queuedRefresh) { state.queuedRefresh = false; void request(true) }
      }
    }
  }
  const load = () => request(false)
  const loadMore = () => request(false)
  const refresh = () => request(true)
  function setTeam(teamId: string) { if (!disposed && (teamId === '0' || isId(teamId)) && teamId !== state.teamId) { scope++; refreshVersion++; Object.assign(state, initialNotificationState(), { teamId }); void load() } }
  function hint(_notificationId?: string, hintTeamId?: string) { if (disposed) return; taskSignal.set(); if (hintTeamId && state.teamId !== '0' && state.teamId !== hintTeamId) return; if (state.loading) state.queuedRefresh = true; else void refresh() }
  function activate() { if (!disposed && taskSignal.pending()) hint() }
  async function markRead(item: TaskNotification) {
    if (disposed || state.reading || item.read_at_unix_ms !== '0') return
    const ticket = scope; state.reading = item.notification_id; state.error = ''
    try {
      const result = await read(item)
      if (disposed || ticket !== scope) return
      if (result.notification_id !== item.notification_id || !decimal(result.read_at_unix_ms, true) || BigInt(result.read_at_unix_ms) < BigInt(item.created_at_unix_ms)) throw new ApiError(502, '通知数据无效，请重试')
      await refresh()
    } catch (error) {
      if (disposed || ticket !== scope || error instanceof StaleRequestError) return
      state.error = errorText(error)
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { scope++; refreshVersion++; state.items = []; state.cursor = ''; state.unreadCount = '0'; state.loaded = false; state.loading = false; state.queuedRefresh = false; state.reading = '' }
    } finally { if (ticket === scope) state.reading = '' }
  }
  function dispose() { if (disposed) return; disposed = true; scope++; refreshVersion++; state.loading = false; state.queuedRefresh = false; state.reading = ''; unsubscribe() }
  return { state, load, loadMore, refresh, setTeam, hint, activate, markRead, dispose }
}
