import { session } from '../auth/session.ts'
import type { createSession } from '../auth/session.ts'

export function createTaskSignal(identity: ReturnType<typeof createSession>) {
  let value = false
  const listeners = new Set<() => void>()
  const emit = () => { for (const listener of listeners) listener() }
  const set = () => { if (!value) { value = true; emit() } }
  const clear = () => { if (value) { value = false; emit() } }
  const unsubscribe = identity.subscribe(clear)
  return {
    pending: () => value,
    set,
    clear,
    subscribe(listener: () => void) { listeners.add(listener); return () => listeners.delete(listener) },
    dispose() { unsubscribe(); listeners.clear(); value = false },
  }
}

export const taskSignal = createTaskSignal(session)

