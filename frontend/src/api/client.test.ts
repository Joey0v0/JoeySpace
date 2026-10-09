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
test('register submits public fields and does not log in after success without data', async () => {
  const session = createSession()
  const client = createApiClient(session, async (url, options) => {
    assert.equal(url, '/api/v1/user/register')
    assert.equal(new Headers(options?.headers).has('Authorization'), false)
    assert.deepEqual(JSON.parse(String(options?.body)), { username: 'alice', password: 'password', nickname: 'Alice' })
    return new Response(JSON.stringify({ code: 0, msg: 'success' }))
  })
  await client.register('alice', 'password', 'Alice')
  assert.equal(session.token(), null)
})
test('register conflict explains username and leaves session unauthenticated', async () => {
  const session = createSession()
  const client = createApiClient(session, async () => reply(null, 409))
  await assert.rejects(client.register('alice', 'password', ''), (error: unknown) => error instanceof ApiError && error.status === 409 && error.message.includes('用户名'))
  assert.equal(session.token(), null)
})
test('registration rejects invalid Unicode lengths and bcrypt byte overflow before HTTP', async () => {
  let requests = 0
  const client = createApiClient(createSession(), async () => { requests++; return new Response(JSON.stringify({ code: 0, msg: 'success' })) })
  for (const [username, password, nickname] of [['ab', 'password', ''], ['alice', '短短', ''], ['alice', '中'.repeat(25), ''], ['alice', 'password', '名'.repeat(65)]]) {
    await assert.rejects(client.register(username!, password!, nickname!), (error: unknown) => error instanceof ApiError && error.status === 400)
  }
  assert.equal(requests, 0)
  await client.register('三字名', '中'.repeat(24), '')
  assert.equal(requests, 1)
})
test('directory reads still reject success envelopes missing data', async () => {
  const client = createApiClient(createSession(), async () => new Response(JSON.stringify({ code: 0, msg: 'success' })))
  await assert.rejects(client.request('/teams'), (error: unknown) => error instanceof ApiError && error.status === 502)
})

test('task reads encode list query and detail path while preserving string IDs', async () => {
  const urls: string[] = []
  const client = createApiClient(createSession(), async url => {
    urls.push(String(url))
    if (String(url).includes('/tasks?')) return reply({ tasks: [{ task_id: '9007199254740993', team_id: '2', team_name: '研发', title: '标题', description: '', creator_id: '3', creator_name: '甲', assignee_id: '4', assignee_name: '乙', status: 0, source_group_id: '0', source_message_id: '0', due_at_unix_ms: '0' }], next_cursor: 'eyJ2ZXJzaW9uIjoxfQ' })
    return reply({ task: { task_id: '9', team_id: '2', team_name: '研发', title: '标题', description: '', creator_id: '3', creator_name: '甲', assignee_id: '4', assignee_name: '乙', status: 1, source_group_id: '7', source_message_id: '8', due_at_unix_ms: '1700000000000' }, can_update_status: true })
  })
  assert.equal((await client.listMyTasks({ view: 'open', teamId: '2', cursor: 'eyJ2ZXJzaW9uIjoxfQ', limit: 20 })).next_cursor, 'eyJ2ZXJzaW9uIjoxfQ')
  assert.equal((await client.getTask('2', '9')).task.task_id, '9')
  assert.deepEqual(urls, ['/api/v1/tasks?view=open&team_id=2&cursor=eyJ2ZXJzaW9uIjoxfQ&limit=20', '/api/v1/teams/2/tasks/9'])
})

test('task reads turn malformed successful data into safe 502 errors', async () => {
  const client = createApiClient(createSession(), async () => reply({ tasks: [{ task_id: 1 }], next_cursor: '0' }))
  await assert.rejects(client.listMyTasks({ view: 'open' }), (error: unknown) => error instanceof ApiError && error.status === 502 && !error.message.includes('task_id'))
})
