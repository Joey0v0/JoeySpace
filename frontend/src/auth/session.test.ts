import test from 'node:test'
import assert from 'node:assert/strict'
import { createSession } from './session.ts'

function storage() {
  const values = new Map<string, string>()
  return { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value) }, removeItem: (key: string) => { values.delete(key) } }
}
test('refresh restores only this tab token and logout removes it', () => {
  const store = storage()
  const first = createSession(store)
  first.setSession('token-a')
  const refreshed = createSession(store)
  assert.equal(refreshed.restoreSession(), 'token-a')
  first.clearSession()
  assert.equal(createSession(store).restoreSession(), null)
  assert.equal(first.token(), null)
})
test('account changes invalidate outstanding scopes, including same token login', () => {
  const session = createSession(storage())
  session.setSession('a')
  const epoch = session.version()
  session.setSession('a')
  assert.notEqual(session.version(), epoch)
  session.clearSession()
  assert.equal(session.token(), null)
})
test('storage failure still permits memory login and logout', () => {
  const denied = () => { throw new Error('storage denied') }
  const session = createSession({ getItem: denied, setItem: denied, removeItem: denied })
  assert.equal(session.restoreSession(), null)
  session.setSession('a')
  assert.equal(session.token(), 'a')
  session.clearSession()
  assert.equal(session.token(), null)
})
