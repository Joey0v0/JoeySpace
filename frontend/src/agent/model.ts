import type { ChatMessage } from '../messages/history.ts'
export interface AgentScope { teamId: string; groupId: string; runId: string }
export interface AgentTrigger {
  message_id: string; team_id: string; group_id: string
  status: 'queued' | 'running' | 'exhausted' | 'completed'; run_id: string
}
export type AssigneeResolution = 'none' | 'matched' | 'not_found' | 'ambiguous' | 'truncated' | 'selected' | 'unassigned'
export interface DraftDeadline {
  text: string; source: 'none' | 'instruction' | 'message'; source_message_id: string
  reference_unix_ms: number; timezone: 'Asia/Shanghai'
  resolution: 'none' | 'parsed' | 'needs_input' | 'selected' | 'unset'
  reason: '' | 'unsupported_expression' | 'invalid_time' | 'invalid_date' | 'nonexistent_local_time' | 'ambiguous_local_time' | 'out_of_range' | 'missing_reference'
  parsed_unix_ms: number; instruction_reference_unix_ms: number
}
export interface TaskDraft {
  revision: string; title: string; description: string; assignee_id: string; assignee_name: string
  assignee_resolution: AssigneeResolution; due_at_unix_ms: number; source_message_id: string; deadline: DraftDeadline
}
export interface DraftItem {
  item_index: number; status: 'waiting_confirmation' | 'creating' | 'skipped' | 'succeeded'
  draft: TaskDraft; task_id: string; reply_status: 'disabled' | 'not_started' | 'unknown' | 'pending' | 'accepted'; reply_msg_id: string
}
export interface DraftCollection { run_id: string; team_id: string; group_id: string; item_count: number; items: DraftItem[] }

const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value)
const decimal = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9]\d*)$/.test(value) && value.length <= 19 && BigInt(value) <= 9223372036854775807n
const positive = (value: unknown): value is string => decimal(value) && value !== '0'
const time = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 && value <= 253402300799999
// Go unicode.IsSpace matches Unicode White_Space, unlike JavaScript trim/\s.
const trim = (value: string) => value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '')
const wellFormed = (value: string) => !/[\uD800-\uDFFF]/u.test(value)
const text = (value: unknown, max: number, min = 0): value is string => typeof value === 'string' && wellFormed(value) && trim(value) === value && [...value].length >= min && [...value].length <= max

// The caller restricts this predicate to the selected, joined team group.
export function isOwnAgentCommand(message: ChatMessage, ownId: string): boolean {
  if (!positive(message.id) || !positive(ownId) || message.from_id !== ownId || (message.sender_type !== 0 && message.sender_type !== 1)
    || message.initiator_id !== '0' || message.content_type !== 1 || typeof message.content !== 'string' || !wellFormed(message.content)) return false
  let rest = trim(message.content)
  for (const token of ['@AI', '整理任务']) {
    if (!rest.startsWith(token)) return false
    rest = rest.slice(token.length)
    if (!/^\p{White_Space}/u.test(rest)) return false
    rest = trim(rest)
  }
  return [...rest].length >= 1 && [...rest].length <= 2000
}

export function decodeAgentTrigger(value: unknown, scope: { teamId: string; groupId: string; messageId: string }): AgentTrigger | null {
  if (!record(value) || !positive(scope.teamId) || !positive(scope.groupId) || !positive(scope.messageId)
    || value.team_id !== scope.teamId || value.group_id !== scope.groupId || value.message_id !== scope.messageId) return null
  if (value.status === 'completed' ? !positive(value.run_id) : !['queued', 'running', 'exhausted'].includes(value.status as string) || value.run_id !== '0') return null
  return value as unknown as AgentTrigger
}

function validDeadline(value: unknown, due: number): value is DraftDeadline {
  if (!record(value) || value.timezone !== 'Asia/Shanghai' || !text(value.text, 200) || !decimal(value.source_message_id)
    || !time(value.reference_unix_ms) || !time(value.parsed_unix_ms) || !time(value.instruction_reference_unix_ms)
    || !['', 'unsupported_expression', 'invalid_time', 'invalid_date', 'nonexistent_local_time', 'ambiguous_local_time', 'out_of_range', 'missing_reference'].includes(value.reason as string)) return false
  const expression = value.source === 'instruction' || value.source === 'message'
  switch (value.source) {
    case 'none':
      if (value.text !== '' || value.source_message_id !== '0' || value.reference_unix_ms !== 0 || value.parsed_unix_ms !== 0 || value.reason !== '') return false
      break
    case 'instruction':
      if (value.text === '' || value.source_message_id !== '0' || value.reference_unix_ms !== value.instruction_reference_unix_ms) return false
      break
    case 'message':
      if (value.text === '' || !positive(value.source_message_id) || value.reference_unix_ms <= 0) return false
      break
    default: return false
  }
  if (expression && !((value.parsed_unix_ms > 0 && value.reason === '') || (value.parsed_unix_ms === 0 && value.reason !== ''))) return false
  if (value.reason === 'missing_reference' && (value.source !== 'instruction' || value.reference_unix_ms !== 0)) return false
  switch (value.resolution) {
    case 'none': return !expression && due === 0
    case 'parsed': return expression && value.parsed_unix_ms > 0 && due === value.parsed_unix_ms
    case 'needs_input': return expression && value.parsed_unix_ms === 0 && value.reason !== '' && due === 0
    case 'selected': return due > 0
    case 'unset': return due === 0
    default: return false
  }
}

function validDraft(value: unknown): value is TaskDraft {
  if (!record(value) || !positive(value.revision) || !text(value.title, 200, 1) || !text(value.description, 2000)
    || !decimal(value.assignee_id) || !text(value.assignee_name, 64) || !decimal(value.source_message_id)
    || !time(value.due_at_unix_ms) || !validDeadline(value.deadline, value.due_at_unix_ms)) return false
  switch (value.assignee_resolution) {
    case 'none': return value.assignee_name === '' && value.assignee_id === '0'
    case 'matched': return value.assignee_name !== '' && positive(value.assignee_id)
    case 'not_found': case 'ambiguous': case 'truncated': return value.assignee_name !== '' && value.assignee_id === '0'
    case 'selected': return positive(value.assignee_id)
    case 'unassigned': return value.assignee_id === '0'
    default: return false
  }
}

function decodeItem(value: unknown, runId: string, index: number): DraftItem | null {
  if (!record(value) || value.item_index !== index || !decimal(value.task_id) || !validDraft(value.draft)) return null
  switch (value.status) {
    case 'waiting_confirmation': case 'creating': case 'skipped':
      if (value.task_id !== '0' || (value.status === 'skipped' && value.reply_status !== 'disabled')) return null
      break
    case 'succeeded': if (!positive(value.task_id)) return null; break
    default: return null
  }
  switch (value.reply_status) {
    case 'disabled': case 'not_started': if (value.reply_msg_id !== '') return null; break
    case 'pending': case 'accepted':
      if (value.status !== 'succeeded' || value.reply_msg_id !== 'bot-task:' + runId + (index > 0 ? ':' + index : '')) return null
      break
    case 'unknown': if (value.status !== 'succeeded' || value.reply_msg_id !== '') return null; break
    default: return null
  }
  if ((value.status === 'creating' || value.status === 'succeeded')
    && (['not_found', 'ambiguous', 'truncated'].includes(value.draft.assignee_resolution) || value.draft.deadline.resolution === 'needs_input')) return null
  return value as unknown as DraftItem
}

function validScope(value: unknown, scope: AgentScope): value is Record<string, unknown> & { item_count: number } {
  return record(value) && positive(scope.runId) && positive(scope.teamId) && positive(scope.groupId)
    && value.run_id === scope.runId && value.team_id === scope.teamId && value.group_id === scope.groupId
    && typeof value.item_count === 'number' && Number.isInteger(value.item_count) && value.item_count >= 1 && value.item_count <= 5
}

export function decodeDraftCollection(value: unknown, scope: AgentScope): DraftCollection | null {
  if (!validScope(value, scope) || !Array.isArray(value.items) || value.items.length !== value.item_count) return null
  const items = Array.from(value.items, (item, index) => decodeItem(item, scope.runId, index))
  if (items.some(item => item === null)) return null
  return { run_id: scope.runId, team_id: scope.teamId, group_id: scope.groupId, item_count: value.item_count, items: items as DraftItem[] }
}

export function decodeDraftItem(value: unknown, scope: AgentScope, index: number): DraftItem | null {
  if (!validScope(value, scope) || !Number.isInteger(index) || index < 0 || index >= value.item_count) return null
  return decodeItem(value.item, scope.runId, index)
}
