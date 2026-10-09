import { ApiError, StaleRequestError, errorText } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'
import { decodeTaskDetail, type TaskDetail, type TaskStatus } from './model.ts'

export type StatusPhase = 'idle' | 'submitting' | 'rechecking' | 'success' | 'retryable' | 'uncertain' | 'conflict' | 'error'
export function initialStatusMutationState() { return { phase: 'idle' as StatusPhase, error: '', status: '', target: null as TaskStatus | null, expected: null as TaskStatus | null } }
type State = ReturnType<typeof initialStatusMutationState>
type Request = (path: string, options?: RequestInit) => Promise<unknown>
interface Hooks { apply(detail: TaskDetail): void; clear(): void; refresh(): Promise<unknown> | unknown }

export function createStatusMutation(request: Request, getTask: (teamId: string, taskId: string) => Promise<TaskDetail | unknown>, identity: ReturnType<typeof createSession>, state: State = initialStatusMutationState(), hooks: Hooks) {
  let scope = 0, selection: { teamId: string; taskId: string } | null = null
  const reset = () => { scope++; selection = null; Object.assign(state, initialStatusMutationState()) }
  const unsubscribe = identity.subscribe(reset)
  async function read(ticket: number, afterFailure: boolean) {
    if (!selection || state.target === null || state.expected === null) return
    state.phase = 'rechecking'; state.status = '正在重新核对任务状态…'; state.error = ''
    try {
      const raw = await getTask(selection.teamId, selection.taskId)
      if (ticket !== scope) return
      const detail = (raw as TaskDetail)?.task ? raw as TaskDetail : decodeTaskDetail(raw)
      if (!detail || detail.task.team_id !== selection.teamId || detail.task.task_id !== selection.taskId) throw new ApiError(502, '任务详情数据无效，请重试')
      hooks.apply(detail)
      if (!afterFailure || detail.task.status === state.target) { state.phase = 'success'; state.status = '任务状态已更新'; state.error = ''; await hooks.refresh(); return }
      if (detail.task.status === state.expected) { state.phase = 'retryable'; state.status = ''; state.error = '结果尚未确认，可使用相同前提重试'; return }
      state.phase = 'conflict'; state.status = ''; state.error = '任务状态已变化，请重新确认'
    } catch (error) {
      if (ticket !== scope || error instanceof StaleRequestError) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { hooks.clear(); state.phase = 'error'; state.error = errorText(error); return }
      state.phase = 'uncertain'; state.status = ''; state.error = '暂时无法核对任务状态；当前不会自动重发'
    }
  }
  async function send(ticket: number) {
    if (!selection || state.target === null || state.expected === null) return
    state.phase = 'submitting'; state.status = '正在更新任务状态…'; state.error = ''
    try {
      await request(`/teams/${selection.teamId}/tasks/${selection.taskId}/status`, { method: 'PUT', body: JSON.stringify({ status: state.target, expected_status: state.expected }) })
      if (ticket !== scope) return
      await read(ticket, false)
    } catch (error) {
      if (ticket !== scope || error instanceof StaleRequestError) return
      if (error instanceof ApiError && error.status === 409) { await read(ticket, true); const checked = state.phase as StatusPhase; if (ticket === scope && checked !== 'uncertain' && checked !== 'error') { state.phase = 'conflict'; state.status = ''; state.error = '任务状态已变化，请重新确认' }; return }
      if (error instanceof ApiError && (error.status === 0 || error.status === 504)) { await read(ticket, true); return }
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) hooks.clear()
      state.phase = 'error'; state.status = ''; state.error = errorText(error)
    }
  }
  async function update(detail: TaskDetail, target: TaskStatus) {
    if (state.phase === 'submitting' || state.phase === 'rechecking' || state.phase === 'retryable' || state.phase === 'uncertain' || target === detail.task.status || !detail.can_update_status) return
    selection = { teamId: detail.task.team_id, taskId: detail.task.task_id }; state.target = target; state.expected = detail.task.status
    await send(++scope)
  }
  async function retry() { if (state.phase === 'retryable') await send(scope) }
  async function recheck() { if (selection) await read(scope, true) }
  function dispose() { scope++; unsubscribe() }
  return { state, update, retry, recheck, reset, dispose }
}
