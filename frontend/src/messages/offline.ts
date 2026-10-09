import { ApiError, StaleRequestError, errorText, isId } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'

export interface OfflineMessage {
  id: string
  msg_id: string
  from_id: string
  to_id: string
  sender_type: 1 | 2
  initiator_id: string
  chat_type: 1 | 2
  content_type: number
  content: string
  created_at: string
}
type Request = (path: string, options?: RequestInit) => Promise<unknown>
const decimal = (value: unknown, zero = false): value is string =>
  (isId(value) || (zero && value === '0')) && BigInt(value) <= 9223372036854775807n
function validMessage(item: unknown): item is OfflineMessage {
  if (!item || typeof item !== 'object') return false
  const value = item as Record<string, unknown>
  return decimal(value.id) && typeof value.msg_id === 'string' && value.msg_id.length > 0 && value.msg_id.length <= 64 &&
    decimal(value.from_id) && decimal(value.to_id) && decimal(value.initiator_id, true) &&
    (value.sender_type === 1 || value.sender_type === 2) &&
    (value.chat_type === 1 || value.chat_type === 2) && Number.isInteger(value.content_type) &&
    typeof value.content === 'string' && typeof value.created_at === 'string' && Number.isFinite(Date.parse(value.created_at))
}

export function createOfflineInbox(request: Request, identity: ReturnType<typeof createSession>, onMessages: (items: OfflineMessage[]) => void | Promise<void>) {
  const state = { loading: false, error: '', morePending: false }
  let scope = 0
  const unsubscribe = identity.subscribe(() => { scope++; state.loading = false; state.error = ''; state.morePending = false })
  async function pull() {
    if (state.loading || !identity.token()) return
    const epoch = scope
    state.loading = true; state.error = ''
    try {
      const result = await request('/message/offline')
      if (epoch !== scope) return
      if (!Array.isArray(result)) throw new ApiError(502, '离线消息数据无效')
      const batch = result.slice(0, 1000)
      if (!batch.every(validMessage)) throw new ApiError(502, '离线消息数据无效')
      await onMessages(batch)
      if (epoch !== scope) return
      if (batch.length) {
        const ids = [...new Set((batch as OfflineMessage[]).map(item => item.id))]
        await request('/message/offline/ack', { method: 'POST', body: JSON.stringify({ message_ids: ids }) })
        if (epoch !== scope) return
      }
      state.morePending = result.length > batch.length
    } catch (error) {
      if (epoch === scope && !(error instanceof StaleRequestError)) state.error = errorText(error)
    } finally { if (epoch === scope) state.loading = false }
  }
  function dispose() { scope++; unsubscribe() }
  return { state, pull, dispose }
}
