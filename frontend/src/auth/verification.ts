import { session } from './session.ts'
import type { createSession } from './session.ts'
import { ApiError, StaleRequestError, errorText, verifySession } from '../api/client.ts'
type Outcome = 'ready' | 'retry' | 'login' | 'stale'
export function createVerificationGate(identity: ReturnType<typeof createSession>, verify: () => Promise<unknown>) {
  const state = { target: '', error: '', busy: false }
  const listeners = new Set<() => void>()
  let ticket = 0
  const notify = () => { for (const listener of listeners) listener() }
  identity.subscribe(() => { ticket++; state.error = ''; state.busy = false; notify() })
  async function check(target: string): Promise<Outcome> {
    const request = ++ticket
    const epoch = identity.version()
    state.target = target; state.busy = true
    notify()
    try {
      if (!identity.token()) return 'login'
      await verify()
      if (request !== ticket || epoch !== identity.version()) return 'stale'
      state.error = ''
      return 'ready'
    } catch (error) {
      if (request !== ticket || epoch !== identity.version() || error instanceof StaleRequestError) return 'stale'
      if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
        identity.clearSession()
        return 'login'
      }
      state.error = errorText(error)
      return 'retry'
    } finally {
      if (request === ticket) { state.busy = false; notify() }
    }
  }
  return {
    state, check,
    retry: () => check(state.target),
    subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener) } },
  }
}
export const verification = createVerificationGate(session, verifySession)
