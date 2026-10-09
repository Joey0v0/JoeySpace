import { ApiError, StaleRequestError, errorText, isId } from '../api/client.ts'
import type { createApiClient } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'
import type { Task, TaskDetail, TaskView } from './model.ts'

export interface TeamOption { team_id: string; name: string; role: number }
interface Page<T> { items: T[]; cursor: string; loaded: boolean; loading: boolean; error: string }
const page = <T>(cursor = '0'): Page<T> => ({ items: [], cursor, loaded: false, loading: false, error: '' })
export function initialTaskWorkspaceState() {
  return { view: 'open' as TaskView, teamId: '0', teams: page<TeamOption>(), tasks: page<Task>(''), taskRetry: null as 'initial' | 'refresh' | 'more' | null, selected: null as { teamId: string; taskId: string } | null, detail: null as TaskDetail | null, detailLoading: false, detailError: '' }
}
type State = ReturnType<typeof initialTaskWorkspaceState>
type Client = Pick<ReturnType<typeof createApiClient>, 'listMyTasks' | 'getTask'> & { request: (path: string, options?: RequestInit, authenticated?: boolean, expectData?: boolean) => Promise<unknown> }
const validCursor = (value: unknown): value is string => value === '0' || isId(value)
const validTeam = (value: unknown): value is TeamOption => !!value && typeof value === 'object' && isId((value as TeamOption).team_id) && typeof (value as TeamOption).name === 'string' && typeof (value as TeamOption).role === 'number'
const merge = <T>(old: T[], next: T[], key: (item: T) => string) => [...new Map([...old, ...next].map(item => [key(item), item])).values()]

export function createTaskWorkspace(client: Client, identity: ReturnType<typeof createSession>, state: State = initialTaskWorkspaceState()) {
  let scope = 0, detailTicket = 0, teamScope = 0
  const unsubscribe = identity.subscribe(() => { scope++; detailTicket++; teamScope++; Object.assign(state, initialTaskWorkspaceState()) })

  async function loadTasks(replace = false) {
    if (state.tasks.loading || (!replace && state.tasks.loaded && state.tasks.cursor === '')) return
    const ticket = scope, current = state.tasks, cursor = replace ? '' : current.cursor
    const operation = replace ? 'refresh' : current.loaded ? 'more' : 'initial'
    current.loading = true; current.error = ''; state.taskRetry = null
    try {
      const result = await client.listMyTasks({ view: state.view, teamId: state.teamId, cursor, limit: 20 })
      if (ticket !== scope) return
      current.items = replace ? result.tasks : merge(current.items, result.tasks, item => item.task_id)
      current.cursor = result.next_cursor; current.loaded = true; state.taskRetry = null
    } catch (error) {
      if (ticket !== scope || error instanceof StaleRequestError) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) {
        resetTasks()
        state.tasks.error = errorText(error)
        state.taskRetry = 'initial'
        return
      }
      current.error = errorText(error); state.taskRetry = operation
    } finally { if (ticket === scope) current.loading = false }
  }
  const refreshTasks = () => loadTasks(true)
  const retryTasks = () => state.taskRetry === 'refresh' ? refreshTasks() : loadTasks()
  function resetTasks() { scope++; state.tasks = page<Task>(''); state.taskRetry = null; state.detail = null; state.detailError = ''; state.detailLoading = false; state.selected = null; detailTicket++ }
  function setView(view: TaskView) { if (view !== state.view) { state.view = view; resetTasks() } }
  function setTeam(teamId: string) { if ((teamId === '0' || isId(teamId)) && teamId !== state.teamId) { state.teamId = teamId; resetTasks() } }

  async function selectTask(teamId?: string, taskId?: string) {
    const ticket = ++detailTicket, currentScope = scope
    state.detail = null; state.detailError = ''; state.selected = null; state.detailLoading = !!teamId || !!taskId
    if (!teamId && !taskId) { state.detailLoading = false; return }
    if (!isId(teamId) || !isId(taskId)) { state.detailLoading = false; state.detailError = '任务地址无效'; return }
    state.selected = { teamId, taskId }
    try {
      const result = await client.getTask(teamId, taskId)
      if (ticket !== detailTicket || currentScope !== scope) return
      if (result.task.team_id !== teamId || result.task.task_id !== taskId) throw new ApiError(502, '任务数据无效，请重试')
      state.detail = result
    } catch (error) {
      if (ticket !== detailTicket || currentScope !== scope || error instanceof StaleRequestError) return
      state.detailError = errorText(error)
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) state.detail = null
    } finally { if (ticket === detailTicket && currentScope === scope) state.detailLoading = false }
  }

  async function loadTeams() {
    const target = state.teams
    if (target.loading || (target.loaded && target.cursor === '0')) return
    const ticket = teamScope
    target.loading = true; target.error = ''
    try {
      const data = await client.request('/teams?after_team_id=' + target.cursor + '&limit=20') as any
      if (ticket !== teamScope) return
      if (!data || !Array.isArray(data.teams) || !data.teams.every(validTeam) || !validCursor(data.next_after_team_id)) throw new ApiError(502, '团队数据无效，请重试')
      target.items = merge(target.items, data.teams, item => item.team_id); target.cursor = data.next_after_team_id; target.loaded = true
    } catch (error) {
      if (ticket !== teamScope || error instanceof StaleRequestError) return
      target.error = errorText(error)
    } finally { if (ticket === teamScope) target.loading = false }
  }
  return { state, loadTasks, refreshTasks, retryTasks, setView, setTeam, selectTask, loadTeams, dispose: unsubscribe }
}
