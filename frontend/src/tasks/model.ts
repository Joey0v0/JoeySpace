export type TaskView = 'open' | 'completed'
export type TaskStatus = 0 | 1 | 2
export interface Task {
  task_id: string; team_id: string; team_name: string; title: string; description: string
  creator_id: string; creator_name: string; assignee_id: string; assignee_name: string
  status: TaskStatus; source_group_id: string; source_message_id: string; due_at_unix_ms: string
}
export interface TaskPage { tasks: Task[]; next_cursor: string }
export interface TaskDetail { task: Task; can_update_status: boolean }
export type TaskGroupKey = 'overdue' | 'soon' | 'later' | 'none' | 'completed'
export interface TaskGroup { key: TaskGroupKey; label: string; tasks: Task[] }

const positive = (value: unknown): value is string => typeof value === 'string' && /^[1-9]\d*$/.test(value)
const nonNegative = (value: unknown): value is string => typeof value === 'string' && /^(0|[1-9]\d*)$/.test(value)
const text = (value: unknown): value is string => typeof value === 'string'
const opaqueCursor = (value: unknown): value is string => typeof value === 'string' && (value === '' || (value.length <= 2048 && /^[A-Za-z0-9_-]+$/.test(value)))
const safeDate = (value: string) => BigInt(value) <= 8640000000000000n

export function decodeTask(value: unknown): Task | null {
  if (!value || typeof value !== 'object') return null
  const item = value as Record<string, unknown>
  if (!positive(item.task_id) || !positive(item.team_id) || !positive(item.creator_id) || !nonNegative(item.assignee_id)
    || !nonNegative(item.source_group_id) || !nonNegative(item.source_message_id) || !nonNegative(item.due_at_unix_ms)
    || !safeDate(item.due_at_unix_ms) || !text(item.team_name) || !text(item.title) || !text(item.description)
    || !text(item.creator_name) || !text(item.assignee_name) || (item.status !== 0 && item.status !== 1 && item.status !== 2)
    || ((item.source_group_id === '0') !== (item.source_message_id === '0'))) return null
  return item as unknown as Task
}

export function decodeTaskPage(value: unknown): TaskPage | null {
  if (!value || typeof value !== 'object') return null
  const page = value as Record<string, unknown>
  if (!Array.isArray(page.tasks) || !opaqueCursor(page.next_cursor)) return null
  const tasks = page.tasks.map(decodeTask)
  if (tasks.some(item => !item)) return null
  return { tasks: tasks as Task[], next_cursor: page.next_cursor }
}

export function decodeTaskDetail(value: unknown): TaskDetail | null {
  if (!value || typeof value !== 'object') return null
  const detail = value as Record<string, unknown>, task = decodeTask(detail.task)
  return task && typeof detail.can_update_status === 'boolean' ? { task, can_update_status: detail.can_update_status } : null
}

export function groupTasks(tasks: Task[], view: TaskView, now = Date.now()): TaskGroup[] {
  if (view === 'completed') return tasks.length ? [{ key: 'completed', label: '已完成', tasks }] : []
  const end = now + 7 * 24 * 60 * 60 * 1000
  const groups: TaskGroup[] = [
    { key: 'overdue', label: '已逾期', tasks: [] }, { key: 'soon', label: '未来 7 天', tasks: [] },
    { key: 'later', label: '稍后', tasks: [] }, { key: 'none', label: '无截止时间', tasks: [] },
  ]
  for (const task of tasks) {
    const due = Number(task.due_at_unix_ms)
    const index = due === 0 ? 3 : due <= now ? 0 : due <= end ? 1 : 2
    groups[index]!.tasks.push(task)
  }
  return groups.filter(group => group.tasks.length)
}

export const taskStatusLabel = (status: TaskStatus) => status === 0 ? '待处理' : status === 1 ? '进行中' : '已完成'
export function formatTaskDue(value: string): string {
  if (value === '0') return '无截止时间'
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }).format(new Date(Number(value)))
}
