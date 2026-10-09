import { ApiError, StaleRequestError, errorText, isId } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'
import { decodeTaskDetail, type TaskDetail } from './model.ts'

export interface CreateForm {
  teamId: string; title: string; description: string; assigneeId: string
  sourceGroupId: string; sourceMessageId: string; dueLocal: string
}
export interface CreateBody {
  title: string; description: string; assignee_id: string; source_group_id: string
  source_message_id: string; due_at_unix_ms: number
}
export interface NormalizedCreate { teamId: string; body: CreateBody }
export type CreatePhase = 'idle' | 'submitting' | 'uncertain' | 'detail-uncertain' | 'success' | 'error'
export function initialCreateState() {
  return { phase: 'idle' as CreatePhase, frozen: false, error: '', status: '', taskId: '', detail: null as TaskDetail | null }
}
type State = ReturnType<typeof initialCreateState>
type Request = (path: string, options?: RequestInit) => Promise<unknown>

function invalid(message: string): never { throw new ApiError(400, message) }
export function shanghaiDateTimeToUnixMs(value: string): number {
  if (!value) return 0
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value)
  if (!match) return invalid('截止时间格式无效')
  const [, ys, mos, ds, hs, mis] = match
  const year = Number(ys), month = Number(mos), day = Number(ds), hour = Number(hs), minute = Number(mis)
  if (year < 1 || year > 9999 || month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59) return invalid('截止时间无效')
  const check = new Date(0)
  check.setUTCFullYear(year, month - 1, day); check.setUTCHours(hour, minute, 0, 0)
  const localAsUTC = check.getTime()
  if (check.getUTCFullYear() !== year || check.getUTCMonth() !== month - 1 || check.getUTCDate() !== day || check.getUTCHours() !== hour || check.getUTCMinutes() !== minute) return invalid('截止时间无效')
  const result = localAsUTC - 8 * 60 * 60 * 1000
  if (!Number.isSafeInteger(result) || result < 1 || result > 253402300799999) return invalid('截止时间超出支持范围')
  return result
}
export function normalizeCreate(form: CreateForm): NormalizedCreate {
  const title = form.title.trim(), description = form.description.trim()
  if (!isId(form.teamId)) return invalid('请选择有效团队')
  if (![...title].length || [...title].length > 200) return invalid('标题需为 1—200 字')
  if ([...description].length > 2000) return invalid('说明最多 2000 字')
  if (form.assigneeId !== '0' && !isId(form.assigneeId)) return invalid('负责人无效')
  const sourceGroupId = form.sourceGroupId || '0', sourceMessageId = form.sourceMessageId || '0'
  const sourceValid = sourceGroupId === '0' ? sourceMessageId === '0' : isId(sourceGroupId) && isId(sourceMessageId)
  if (!sourceValid) return invalid('讨论来源无效，请重新进入')
  return { teamId: form.teamId, body: { title, description, assignee_id: form.assigneeId, source_group_id: sourceGroupId, source_message_id: sourceMessageId, due_at_unix_ms: shanghaiDateTimeToUnixMs(form.dueLocal) } }
}
export function randomIdempotencyKey(): string {
  if (!globalThis.crypto?.getRandomValues) return invalid('当前浏览器无法安全生成请求标识')
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2, '0')).join('')
}

export function createTaskCreator(request: Request, identity: ReturnType<typeof createSession>, state: State = initialCreateState(), keyFactory = randomIdempotencyKey) {
  let scope = 0
  let attempt: { key: string; normalized: NormalizedCreate; bodyText: string } | null = null
  const reset = () => { scope++; attempt = null; Object.assign(state, initialCreateState()) }
  const unsubscribe = identity.subscribe(reset)
  const definite = (error: unknown) => error instanceof ApiError && [400, 401, 403, 404, 409, 503].includes(error.status)
  async function readDetail(ticket: number) {
    if (!attempt || !state.taskId) return
    state.phase = 'submitting'; state.status = '正在核对已创建任务…'; state.error = ''
    try {
      const decoded = decodeTaskDetail(await request(`/teams/${attempt.normalized.teamId}/tasks/${state.taskId}`))
      if (ticket !== scope) return
      if (!decoded || decoded.task.team_id !== attempt.normalized.teamId || decoded.task.task_id !== state.taskId) throw new ApiError(502, '任务详情数据无效，请重试')
      state.detail = decoded; state.phase = 'success'; state.frozen = false; state.status = '任务已创建'
    } catch (error) {
      if (ticket !== scope || error instanceof StaleRequestError) return
      state.phase = 'detail-uncertain'; state.frozen = true; state.error = errorText(error) + '；任务已经返回标识，只会重新读取详情'
    }
  }
  async function post(ticket: number) {
    if (!attempt) return
    state.phase = 'submitting'; state.frozen = true; state.error = ''; state.status = '正在创建任务…'
    try {
      const data = await request(`/teams/${attempt.normalized.teamId}/tasks`, { method: 'POST', headers: { 'Idempotency-Key': attempt.key }, body: attempt.bodyText }) as { task_id?: unknown }
      if (ticket !== scope) return
      if (!isId(data?.task_id)) throw new ApiError(502, '创建结果无效，请核对并重试')
      state.taskId = data.task_id
      await readDetail(ticket)
    } catch (error) {
      if (ticket !== scope || error instanceof StaleRequestError) return
      if (definite(error)) {
        state.phase = 'error'; state.frozen = false; attempt = null
        state.error = error instanceof ApiError && error.status === 409 ? '请求键已被其他创建内容占用，请修改后重新提交' : errorText(error)
      } else {
        state.phase = 'uncertain'; state.frozen = true; state.error = '创建结果尚未确认，请使用原请求核对并重试'
      }
      state.status = ''
    }
  }
  async function submit(form: CreateForm) {
    if (state.frozen || state.phase === 'submitting') return
    try {
      const normalized = normalizeCreate(form), key = keyFactory()
      if (!/^[A-Za-z0-9._~-]{1,64}$/.test(key)) throw new ApiError(400, '无法生成合法请求标识')
      attempt = { key, normalized, bodyText: JSON.stringify(normalized.body) }
      const ticket = ++scope
      await post(ticket)
    } catch (error) {
      if (error instanceof StaleRequestError) return
      state.phase = 'error'; state.frozen = false; state.status = ''; state.error = errorText(error)
    }
  }
  async function retry() {
    if (!attempt || state.phase === 'submitting') return
    const ticket = scope
    if (state.taskId) await readDetail(ticket)
    else await post(ticket)
  }
  function dispose() { scope++; unsubscribe() }
  return { state, submit, retry, reset, dispose }
}
