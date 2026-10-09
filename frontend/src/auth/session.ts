interface TabStorage { getItem(key: string): string | null; setItem(key: string, value: string): void; removeItem(key: string): void }
const key = 'joeyspace.session'
export function createSession(storage?: TabStorage) {
  let current: string | null = null
  let epoch = 0
  const listeners = new Set<() => void>()
  function notify() { epoch++; for (const listener of listeners) listener() }
  return {
    token: () => current,
    version: () => epoch,
    subscribe(listener: () => void) { listeners.add(listener); return () => { listeners.delete(listener) } },
    restoreSession() {
      try { current = storage?.getItem(key) || null } catch { current = null }
      notify()
      return current
    },
    setSession(token: string) {
      current = token || null
      try { if (current) storage?.setItem(key, current); else storage?.removeItem(key) } catch { /* memory session remains available */ }
      notify()
    },
    clearSession() {
      current = null
      try { storage?.removeItem(key) } catch { /* inaccessible browser storage */ }
      notify()
    },
  }
}
function tabStorage() { try { return globalThis.sessionStorage } catch { return undefined } }
export const session = createSession(tabStorage())
export const restoreSession = () => session.restoreSession()
export const setSession = (token: string) => session.setSession(token)
export const clearSession = () => session.clearSession()
