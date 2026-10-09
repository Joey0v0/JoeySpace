import test from 'node:test'
import assert from 'node:assert/strict'
import { createSession } from '../auth/session.ts'
import { createApiClient, ApiError, StaleRequestError } from './client.ts'

const reply = (data: unknown, status = 200) => new Response(JSON.stringify({ code: status === 200 ? 0 : status, msg: 'response', data }), { status })
test('request uses header identity and preserves large string IDs', async () => {
  const session = createSession()
  session.setSession('secret')
  const client = createApiClient(session, async (url, options) => {
    assert.equal(url, '/api/v1/user/info')
    assert.equal(new Headers(options?.headers).get('Authorization'), 'Bearer secret')
    return reply({ id: '9007199254740993', username: 'a', nickname: 'A' })
  })
  assert.equal((await client.getMyInfo()).id, '9007199254740993')
})
test('403 keeps identity while 401 clears identity', async () => {
  const session = createSession()
  session.setSession('a')
  let status = 403
  const client = createApiClient(session, async () => reply(null, status))
  await assert.rejects(client.request('/teams'), (error: unknown) => error instanceof ApiError && error.status === 403)
  assert.equal(session.token(), 'a')
  status = 401
  await assert.rejects(client.request('/teams'), ApiError)
  assert.equal(session.token(), null)
})
test('old success and old 401 cannot affect switched account', async () => {
  for (const status of [200, 401]) {
    const session = createSession()
    session.setSession('old')
    let complete!: (response: Response) => void
    const client = createApiClient(session, () => new Promise(resolve => { complete = resolve }))
    const request = client.request('/teams')
    session.setSession('new')
    complete(reply({ teams: [] }, status))
    await assert.rejects(request, StaleRequestError)
    assert.equal(session.token(), 'new')
  }
})
test('login has no stored password or token in URL and requires valid profile', async () => {
  const session = createSession()
  const urls: string[] = []
  const client = createApiClient(session, async (url, options) => {
    urls.push(String(url))
    if (String(url).endsWith('/login')) {
      assert.equal(new Headers(options?.headers).has('Authorization'), false)
      assert.deepEqual(JSON.parse(String(options?.body)), { username: 'alice', password: 'password' })
      return reply({ token: 'new-secret' })
    }
    return reply({ id: '9007199254740993', username: 'alice', nickname: 'Alice' })
  })
  const profile = await client.login('alice', 'password')
  assert.equal(profile.id, '9007199254740993')
  assert.deepEqual(urls, ['/api/v1/user/login', '/api/v1/user/info'])
  assert.equal(session.token(), 'new-secret')
})
test('network failure and malformed profile do not become authenticated success', async () => {
  const session = createSession()
  const client = createApiClient(session, async (url) => String(url).endsWith('/login') ? reply({ token: 't' }) : reply({ id: 9007199254740992, username: 'a', nickname: '' }))
  await assert.rejects(client.login('a', 'p'), ApiError)
  assert.equal(session.token(), null)
  const network = createApiClient(session, async () => { throw new Error('secret raw message') })
  await assert.rejects(network.request('/teams'), (error: unknown) => error instanceof ApiError && !error.message.includes('secret'))
})
