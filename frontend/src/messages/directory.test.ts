import test from 'node:test'
import assert from 'node:assert/strict'
import { createDirectory, initialDirectoryState } from './directory.ts'
import { createSession } from '../auth/session.ts'
import { ApiError } from '../api/client.ts'
const team = { team_id: '9007199254740993', name: '研发', role: 2 }
const direct = { peer_id: '9007199254740995', display_name: 'Alice', last_message_id: '9007199254740997' }
test('successful empty directory differs from service failure', async () => {
  const identity = createSession()
  const state = initialDirectoryState()
  const directory = createDirectory(async () => ({ teams: [], next_after_team_id: '0' }), identity, state)
  await directory.loadTeams()
  assert.equal(state.teams.loaded, true)
  assert.equal(state.teams.error, '')
  const broken = createDirectory(async () => { throw new ApiError(503, '服务不可用') }, identity)
  await broken.loadTeams()
  assert.equal(broken.state.teams.loaded, false)
  assert.equal(broken.state.teams.error, '服务不可用')
})
test('direct snapshot pagination preserves large IDs and loaded items on failure', async () => {
  const paths: string[] = []
  let fail = false
  const directory = createDirectory(async (path: string) => {
    paths.push(path)
    if (fail) throw new ApiError(504, '超时')
    return { conversations: [direct], snapshot_upper_message_id: '9007199254740999', next_before_last_message_id: '9007199254740997' }
  }, createSession())
  await directory.loadDirects()
  fail = true
  await directory.loadDirects()
  assert.equal(paths[1], '/me/direct-conversations?snapshot_upper_message_id=9007199254740999&before_last_message_id=9007199254740997&limit=20')
  assert.deepEqual(directory.state.directs.items, [direct])
  assert.equal(directory.state.directs.cursor, '9007199254740997')
  assert.equal(directory.state.directs.error, '超时')
})
test('rapid detail navigation ignores the former response', async () => {
  const pending = new Map<string, (data: unknown) => void>()
  const directory = createDirectory((path: string) => new Promise(resolve => pending.set(path, resolve)), createSession())
  const old = directory.select('direct:1')
  const next = directory.select('direct:2')
  pending.get('/me/direct-conversations/2')!({ conversation: { ...direct, peer_id: '2' } })
  await next
  pending.get('/me/direct-conversations/1')!({ conversation: { ...direct, peer_id: '1' } })
  await old
  assert.equal(directory.state.current?.key, 'direct:2')
})
test('resource denial clears selected data without clearing account', async () => {
  for (const status of [403, 404]) {
    const session = createSession()
    session.setSession('a')
    const directory = createDirectory(async () => { throw new ApiError(status, '拒绝') }, session)
    await directory.select('group:3:4')
    assert.equal(directory.state.current, null)
    assert.equal(directory.state.detailError, '拒绝')
    assert.equal(session.token(), 'a')
  }
})
test('account switch clears all private data and ignores outstanding directory response', async () => {
  const session = createSession()
  session.setSession('old')
  let complete!: (data: unknown) => void
  const directory = createDirectory(() => new Promise(resolve => { complete = resolve }), session)
  const request = directory.loadTeams()
  session.setSession('new')
  complete({ teams: [team], next_after_team_id: '0' })
  await request
  assert.deepEqual(directory.state.teams.items, [])
  assert.equal(directory.state.teams.loaded, false)
})
test('unjoined group never auto joins; explicit join rechecks detail', async () => {
  let joined = false
  const paths: string[] = []
  const directory = createDirectory(async (path: string, options?: RequestInit) => {
    paths.push(path)
    if (options?.method === 'POST') { joined = true; return { group_id: '4' } }
    return { group: { group_id: '4', owner_id: '1', name: '讨论', joined } }
  }, createSession())
  await directory.select('group:3:4')
  assert.equal(directory.state.current?.joined, false)
  assert.equal(paths.length, 1)
  await directory.joinCurrent()
  assert.equal(directory.state.current?.joined, true)
  assert.deepEqual(paths, ['/teams/3/groups/4', '/teams/3/groups/4/join', '/teams/3/groups/4'])
})
test('invalid numeric IDs are rejected before any partial page is committed', async () => {
  const directory = createDirectory(async () => ({ teams: [team, { ...team, team_id: 4 }], next_after_team_id: '0' }), createSession())
  await directory.loadTeams()
  assert.equal(directory.state.teams.loaded, false)
  assert.deepEqual(directory.state.teams.items, [])
  assert.ok(directory.state.teams.error)
})
test('group directory revocation clears the selected group from that team', async () => {
  let revoked = false
  const directory = createDirectory(async (path: string) => {
    if (revoked) throw new ApiError(403, '已离队')
    if (path.includes('?')) return { groups: [{ group_id: '4', owner_id: '1', name: '讨论', joined: true }], next_after_group_id: '4' }
    return { group: { group_id: '4', owner_id: '1', name: '讨论', joined: true } }
  }, createSession())
  await directory.loadGroups('3')
  await directory.select('group:3:4')
  revoked = true
  await directory.loadGroups('3')
  assert.equal(directory.state.current, null)
  assert.equal(directory.state.detailError, '已离队')
})
