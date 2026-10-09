import { ApiError, type createApiClient } from '../api/client.ts'
import { decodeAgentTrigger, decodeDraftCollection, decodeDraftItem, type AgentScope, type DraftDeadline } from './model.ts'
export interface TriggerScope { teamId: string; groupId: string; messageId: string }
export interface TextEdit { title: string; description: string; expected_revision: string }
export interface AssigneeEdit { assignee_id: string; expected_revision: string }
export interface DeadlineEdit { due_at_unix_ms: number; expected_revision: string }
export interface ConfirmDraft {
  expected_title: string; expected_description: string; expected_revision: string
  expected_assignee_id: string; expected_due_at_unix_ms: number
  expected_deadline_resolution: DraftDeadline['resolution']
}
const decimal = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9]\d*)$/.test(value) && value.length <= 19 && BigInt(value) <= 9223372036854775807n
const positive = (value: unknown) => decimal(value) && value !== '0'
const trim = (value: string) => value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '')
const text = (value: unknown, min: number, max: number): value is string => typeof value === 'string' && !/[\uD800-\uDFFF]/u.test(value) && [...value].length >= min && [...value].length <= max
const time = (value: unknown) => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 && value <= 253402300799999
function requireInput(valid: unknown): asserts valid { if (!valid) throw new ApiError(400, 'AI 输入或地址无效，请检查后重试') }
function decoded<T>(result: T | null): T { if (result === null) throw new ApiError(502, 'AI 服务返回的数据无效，请重试'); return result }
function group(scope: { teamId: string; groupId: string }) { requireInput(scope && positive(scope.teamId) && positive(scope.groupId)) }
function run(scope: AgentScope) { group(scope); requireInput(positive(scope.runId)) }
function itemPath(scope: AgentScope, index: number) {
  run(scope); requireInput(Number.isInteger(index) && index >= 0 && index <= 4)
  return '/agent/runs/' + scope.runId + '/drafts/' + index
}
export function createAgentApi(request: ReturnType<typeof createApiClient>['request']) {
  async function ask(teamId: string, groupId: string, question: string): Promise<{ answer: string }> {
    group({ teamId, groupId }); requireInput(typeof question === 'string')
    question = trim(question); requireInput(text(question, 1, 2000))
    const value = await request<unknown>('/teams/' + teamId + '/groups/' + groupId + '/ask', { method: 'POST', body: JSON.stringify({ question }) }, true, true, 25000)
    if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).length !== 1 || !('answer' in value) || !text(value.answer, 1, Number.MAX_SAFE_INTEGER) || !trim(value.answer)) throw new ApiError(502, 'AI 服务返回的数据无效，请重试')
    return { answer: value.answer }
  }
  async function trigger(scope: TriggerScope) {
    group(scope); requireInput(positive(scope.messageId))
    return decoded(decodeAgentTrigger(await request<unknown>('/teams/' + scope.teamId + '/groups/' + scope.groupId + '/agent-triggers/' + scope.messageId, { method: 'GET' }, true, true, 18000), scope))
  }
  async function collection(scope: AgentScope) {
    run(scope)
    return decoded(decodeDraftCollection(await request<unknown>('/agent/runs/' + scope.runId + '/drafts', { method: 'GET' }, true, true, 18000), scope))
  }
  async function operation(scope: AgentScope, index: number, method: string, suffix = '', body?: unknown, timeout = 18000) {
    const path = itemPath(scope, index) + suffix
    const options: RequestInit = { method }
    if (body !== undefined) options.body = JSON.stringify(body)
    return decoded(decodeDraftItem(await request<unknown>(path, options, true, true, timeout), scope, index))
  }
  async function editText(scope: AgentScope, index: number, body: TextEdit) {
    requireInput(body && typeof body.title === 'string' && typeof body.description === 'string' && positive(body.expected_revision))
    const title = trim(body.title), description = trim(body.description)
    requireInput(text(title, 1, 200) && text(description, 0, 2000))
    return operation(scope, index, 'PUT', '', { title, description, expected_revision: body.expected_revision })
  }
  async function selectAssignee(scope: AgentScope, index: number, body: AssigneeEdit) {
    requireInput(body && decimal(body.assignee_id) && positive(body.expected_revision))
    return operation(scope, index, 'PUT', '/assignee', { assignee_id: body.assignee_id, expected_revision: body.expected_revision })
  }
  async function editDeadline(scope: AgentScope, index: number, body: DeadlineEdit) {
    requireInput(body && time(body.due_at_unix_ms) && positive(body.expected_revision))
    return operation(scope, index, 'PUT', '/deadline', { due_at_unix_ms: body.due_at_unix_ms, expected_revision: body.expected_revision })
  }
  async function confirm(scope: AgentScope, index: number, body: ConfirmDraft) {
    requireInput(body && text(body.expected_title, 1, 200) && trim(body.expected_title) === body.expected_title && text(body.expected_description, 0, 2000) && trim(body.expected_description) === body.expected_description && positive(body.expected_revision) && decimal(body.expected_assignee_id) && time(body.expected_due_at_unix_ms) && ['none', 'parsed', 'needs_input', 'selected', 'unset'].includes(body.expected_deadline_resolution))
    return operation(scope, index, 'POST', '/confirm', { expected_title: body.expected_title, expected_description: body.expected_description, expected_revision: body.expected_revision, expected_assignee_id: body.expected_assignee_id, expected_due_at_unix_ms: body.expected_due_at_unix_ms, expected_deadline_resolution: body.expected_deadline_resolution }, 23000)
  }
  async function skip(scope: AgentScope, index: number, body: { expected_revision: string }) {
    requireInput(body && positive(body.expected_revision))
    return operation(scope, index, 'POST', '/skip', { expected_revision: body.expected_revision })
  }
  return { ask, trigger, collection, item: (scope: AgentScope, index: number) => operation(scope, index, 'GET'), editText, selectAssignee, editDeadline, confirm, skip, retryReply: (scope: AgentScope, index: number) => operation(scope, index, 'POST', '/reply/retry', undefined, 22000) }
}
