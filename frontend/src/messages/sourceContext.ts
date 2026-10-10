import { ApiError, StaleRequestError, errorText, isId } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'

export interface SourceMessage { id: string; msg_id: string; from_id: string; sender_type: 1 | 2; initiator_id: string; content_type: number; content: string; created_at_unix_ms: string; mentioned_user_ids: string[] }
export interface SourceContext { messages: SourceMessage[]; target_message_id: string }
const nonNegative = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9]\d*)$/.test(value)
const safeTime = (value: string) => BigInt(value) <= 8640000000000000n
function decodeMessage(value: unknown): SourceMessage | null {
  if (!value || typeof value !== 'object') return null
  const item = value as Record<string, unknown>
  if (!isId(item.id) || typeof item.msg_id !== 'string' || !item.msg_id || !isId(item.from_id) || (item.sender_type !== 1 && item.sender_type !== 2)
    || !nonNegative(item.initiator_id) || (item.sender_type === 2 ? item.initiator_id === '0' : item.initiator_id !== '0')
    || !Number.isInteger(item.content_type) || (item.content_type as number) < 0 || typeof item.content !== 'string'
    || !nonNegative(item.created_at_unix_ms) || !safeTime(item.created_at_unix_ms)
    || (item.mentioned_user_ids !== undefined && (!Array.isArray(item.mentioned_user_ids) || item.mentioned_user_ids.length > 10 || !item.mentioned_user_ids.every(isId) || new Set(item.mentioned_user_ids).size !== item.mentioned_user_ids.length))) return null
  const mentions = item.mentioned_user_ids ?? []
  if (new Set(mentions as string[]).size !== (mentions as string[]).length) return null
  return { ...item, mentioned_user_ids: mentions } as SourceMessage
}
export function decodeSourceContext(value: unknown, expectedTarget: string): SourceContext | null {
  if (!value || typeof value !== 'object' || !isId(expectedTarget)) return null
  const raw = value as Record<string, unknown>
  if (raw.target_message_id !== expectedTarget || !Array.isArray(raw.messages) || raw.messages.length < 1 || raw.messages.length > 41) return null
  const messages = raw.messages.map(decodeMessage)
  if (messages.some(item => !item)) return null
  const decoded = messages as SourceMessage[]
  for (let index = 1; index < decoded.length; index++) if (BigInt(decoded[index - 1]!.id) >= BigInt(decoded[index]!.id)) return null
  if (decoded.filter(item => item.id === expectedTarget).length !== 1) return null
  return { messages: decoded, target_message_id: expectedTarget }
}
export function initialSourceContextState() { return { context: null as SourceContext | null, loading: false, error: '', selection: null as { teamId: string; groupId: string; messageId: string } | null } }
type State = ReturnType<typeof initialSourceContextState>
type Request = (path: string, options?: RequestInit) => Promise<unknown>
export function createSourceContext(request: Request, identity: ReturnType<typeof createSession>, state: State = initialSourceContextState()) {
  let scope = 0
  const clear = () => { scope++; Object.assign(state, initialSourceContextState()) }
  const unsubscribe = identity.subscribe(clear)
  async function load(teamId: string, groupId: string, messageId: string) {
    clear()
    state.selection = { teamId, groupId, messageId }
    if (!isId(teamId) || !isId(groupId) || !isId(messageId)) { state.error = '讨论来源地址无效'; return }
    const ticket = scope; state.loading = true
    try {
      const value = await request(`/teams/${teamId}/groups/${groupId}/messages/${messageId}/context`)
      if (ticket !== scope) return
      const context = decodeSourceContext(value, messageId)
      if (!context) throw new ApiError(502, '讨论来源数据无效，请重试')
      state.context = context
    } catch (error) {
      if (ticket !== scope || error instanceof StaleRequestError) return
      state.context = null; state.error = errorText(error)
    } finally { if (ticket === scope) state.loading = false }
  }
  function dispose() { scope++; unsubscribe() }
  return { state, load, clear, dispose }
}
export function canCreateTaskFromMessage(kind: 'group' | 'direct', message: { id?: unknown; msg_id?: unknown }) { return kind === 'group' && isId(message.id) && typeof message.msg_id === 'string' && !!message.msg_id }
export function taskFromMessageRoute(teamId: string, groupId: string, messageId: string) { return `/tasks/new?team_id=${teamId}&source_group_id=${groupId}&source_message_id=${messageId}` }
export function sourceFocusRoute(teamId: string, groupId: string, messageId: string) { return `/messages/teams/${teamId}/groups/${groupId}?focus_message_id=${messageId}` }
