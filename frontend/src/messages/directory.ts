import { ApiError, StaleRequestError, errorText, isId } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'
export interface Team { team_id: string; name: string; role: number }
export interface Group { group_id: string; owner_id: string; name: string; joined: boolean }
export interface Direct { peer_id: string; display_name: string; last_message_id: string }
export interface Selection { key: string; kind: 'group' | 'direct'; title: string; teamId?: string; groupId?: string; joined?: boolean }
export interface Page<T> { items: T[]; cursor: string; loaded: boolean; loading: boolean; error: string }
function page<T>(): Page<T> { return { items: [], cursor: '0', loaded: false, loading: false, error: '' } }
export function initialDirectoryState() {
  return { teams: page<Team>(), groups: {} as Record<string, Page<Group>>, directs: page<Direct>(), snapshot: '0', current: null as Selection | null, selectedKey: undefined as string | undefined, detailLoading: false, detailError: '', joining: false }
}
type State = ReturnType<typeof initialDirectoryState>
type Request = (path: string, options?: RequestInit, authenticated?: boolean, expectData?: boolean) => Promise<any>
function cursor(value: unknown): value is string { return value === '0' || isId(value) }
function invalid() { throw new ApiError(502, '目录数据无效，请重试') }
const isTeam = (item: Team) => item && isId(item.team_id) && typeof item.name === 'string' && typeof item.role === 'number'
const isGroup = (item: Group) => item && isId(item.group_id) && isId(item.owner_id) && typeof item.name === 'string' && typeof item.joined === 'boolean'
const isDirect = (item: Direct) => item && isId(item.peer_id) && isId(item.last_message_id) && typeof item.display_name === 'string'
function merge<T>(old: T[], next: T[], id: (item: T) => string): T[] {
  const values = new Map(old.map(item => [id(item), item]))
  next.forEach(item => values.set(id(item), item))
  return [...values.values()]
}
export function createDirectory(request: Request, identity: ReturnType<typeof createSession>, state: State = initialDirectoryState()) {
  let scope = 0
  let detail = 0
  const unsubscribe = identity.subscribe(() => {
    scope++; detail++
    Object.assign(state, initialDirectoryState())
  })
  async function loadPage<T>(target: Page<T>, path: string, itemsKey: string, cursorKey: string, validate: (item: T) => boolean, id: (item: T) => string, directPage = false) {
    if (target.loading || (target.loaded && target.cursor === '0')) return
    const epoch = scope
    target.loading = true; target.error = ''
    try {
      const data = await request(path)
      if (epoch !== scope) return
      if (!data || !Array.isArray(data[itemsKey]) || !data[itemsKey].every(validate) || !cursor(data[cursorKey])) invalid()
      if (directPage && (!cursor(data.snapshot_upper_message_id) || (state.directs.loaded && state.snapshot !== data.snapshot_upper_message_id))) invalid()
      target.items = merge(target.items, data[itemsKey], id)
      target.cursor = data[cursorKey]; target.loaded = true
      if (directPage) state.snapshot = data.snapshot_upper_message_id
    } catch (error) {
      if (epoch !== scope || error instanceof StaleRequestError) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { target.items = []; target.loaded = false; target.cursor = '0' }
      const selected = state.selectedKey?.split(':')
      if (error instanceof ApiError && error.status === 403 && selected?.[0] === 'group' && path.startsWith('/teams/' + selected[1] + '/groups?')) {
        detail++
        state.current = null
        state.detailLoading = false
        state.detailError = errorText(error)
      }
      target.error = errorText(error)
    } finally { if (epoch === scope) target.loading = false }
  }
  function loadTeams() {
    return loadPage(state.teams, '/teams?after_team_id=' + state.teams.cursor + '&limit=20', 'teams', 'next_after_team_id', isTeam, item => item.team_id)
  }
  function loadGroups(teamId: string) {
    if (!isId(teamId)) return Promise.resolve()
    state.groups[teamId] ??= page<Group>()
    const target = state.groups[teamId]!
    return loadPage(target, '/teams/' + teamId + '/groups?after_group_id=' + target.cursor + '&limit=20', 'groups', 'next_after_group_id', isGroup, item => item.group_id)
  }
  function loadDirects() {
    return loadPage(state.directs, '/me/direct-conversations?snapshot_upper_message_id=' + state.snapshot + '&before_last_message_id=' + state.directs.cursor + '&limit=20', 'conversations', 'next_before_last_message_id', isDirect, item => item.peer_id, true)
  }
  async function select(key?: string) {
    const ticket = ++detail
    const epoch = scope
    state.selectedKey = key; state.current = null; state.detailError = ''; state.detailLoading = !!key; state.joining = false
    if (!key) return
    const pieces = key.split(':')
    const kind = pieces[0]
    if ((kind !== 'direct' && kind !== 'group') || pieces.length !== (kind === 'group' ? 3 : 2) || !pieces.slice(1).every(isId)) {
      state.detailLoading = false; state.detailError = '会话地址无效'; return
    }
    const path = kind === 'group' ? '/teams/' + pieces[1] + '/groups/' + pieces[2] : '/me/direct-conversations/' + pieces[1]
    try {
      const data = await request(path)
      if (epoch !== scope || ticket !== detail) return
      if (kind === 'group') {
        const group: Group = data?.group
        if (!isGroup(group) || group.group_id !== pieces[2]) invalid()
        state.current = { key, kind, title: group.name, teamId: pieces[1], groupId: pieces[2], joined: group.joined }
        const groupPage = state.groups[pieces[1]!]
        if (groupPage) groupPage.items = groupPage.items.map(item => item.group_id === group.group_id ? group : item)
      } else {
        const direct: Direct = data?.conversation
        if (!isDirect(direct) || direct.peer_id !== pieces[1]) invalid()
        state.current = { key, kind: 'direct', title: direct.display_name }
      }
    } catch (error) {
      if (epoch !== scope || ticket !== detail || error instanceof StaleRequestError) return
      state.detailError = errorText(error)
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) {
        if (kind === 'group') {
          if (error.status === 403) delete state.groups[pieces[1]!]
          else if (state.groups[pieces[1]!]) state.groups[pieces[1]!]!.items = state.groups[pieces[1]!]!.items.filter(item => item.group_id !== pieces[2])
        } else state.directs.items = state.directs.items.filter(item => item.peer_id !== pieces[1])
      }
    } finally { if (epoch === scope && ticket === detail) state.detailLoading = false }
  }
  async function joinCurrent() {
    const current = state.current
    if (!current || current.kind !== 'group' || current.joined || state.joining) return
    const epoch = scope; const ticket = detail
    state.joining = true; state.detailError = ''
    try {
      await request('/teams/' + current.teamId + '/groups/' + current.groupId + '/join', { method: 'POST' }, true, false)
      if (epoch === scope && ticket === detail) await select(current.key)
    } catch (error) {
      if (epoch !== scope || ticket !== detail || error instanceof StaleRequestError) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { state.current = null; delete state.groups[current.teamId!] }
      state.detailError = errorText(error)
    } finally { if (epoch === scope && (ticket === detail || state.selectedKey === current.key)) state.joining = false }
  }
  return { state, loadTeams, loadGroups, loadDirects, select, joinCurrent, dispose: unsubscribe }
}
