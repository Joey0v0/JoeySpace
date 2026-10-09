import assert from 'node:assert/strict'
import test from 'node:test'
import { createSession } from '../auth/session.ts'
import { createTaskSignal } from './taskSignal.ts'

test('tracks only a pending hint and clears it when identity changes', () => {
  const identity = createSession({ getItem: () => null, setItem() {}, removeItem() {} })
  const signal = createTaskSignal(identity)
  let changes = 0; const stop = signal.subscribe(() => { changes++ })
  signal.set(); signal.set()
  assert.equal(signal.pending(), true); assert.equal(changes, 1)
  signal.clear(); assert.equal(signal.pending(), false)
  signal.set(); identity.setSession('new')
  assert.equal(signal.pending(), false)
  stop(); signal.dispose()
})
