import test from 'node:test'
import assert from 'node:assert/strict'
import { createVerificationGate } from './verification.ts'
import { createSession } from './session.ts'
import { ApiError } from '../api/client.ts'

test('temporary profile failure preserves tab identity and deep link until explicit retry', async () => {
  const session = createSession()
  session.setSession('restored-token')
  let unavailable = true
  const gate = createVerificationGate(session, async () => {
    if (unavailable) throw new ApiError(503, '服务暂时不可用')
  })
  assert.equal(await gate.check('/messages/teams/3/groups/4'), 'retry')
  assert.equal(session.token(), 'restored-token')
  assert.equal(gate.state.target, '/messages/teams/3/groups/4')
  assert.equal(gate.state.error, '服务暂时不可用')
  unavailable = false
  assert.equal(await gate.retry(), 'ready')
  assert.equal(gate.state.error, '')
})
test('invalid or forbidden profile clears identity and requests login', async () => {
  for (const status of [401, 403]) {
    const session = createSession()
    session.setSession('invalid')
    const gate = createVerificationGate(session, async () => { throw new ApiError(status, '身份无效') })
    assert.equal(await gate.check('/messages/direct/4'), 'login')
    assert.equal(session.token(), null)
    assert.equal(gate.state.error, '')
  }
})
test('late profile failure cannot replace a switched account verification state', async () => {
  const session = createSession()
  session.setSession('old')
  let fail!: (error: unknown) => void
  const gate = createVerificationGate(session, () => new Promise((_resolve, reject) => { fail = reject }))
  const pending = gate.check('/messages/direct/4')
  session.setSession('new')
  fail(new ApiError(503, '旧账号失败'))
  assert.equal(await pending, 'stale')
  assert.equal(gate.state.error, '')
  assert.equal(session.token(), 'new')
})
