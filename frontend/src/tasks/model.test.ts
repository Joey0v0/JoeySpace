import test from 'node:test'
import assert from 'node:assert/strict'
import { decodeTask, decodeTaskPage, groupTasks, type Task } from './model.ts'

const raw = (overrides: Record<string, unknown> = {}) => ({
  task_id: '9007199254740993', team_id: '2', team_name: '研发', title: '整理发布清单', description: '逐项核对',
  creator_id: '3', creator_name: '张三', assignee_id: '4', assignee_name: '李四', status: 0,
  source_group_id: '0', source_message_id: '0', due_at_unix_ms: '0', ...overrides,
})

test('task decoder preserves large decimal strings and rejects invalid boundaries', () => {
  assert.equal(decodeTask(raw())?.task_id, '9007199254740993')
  assert.equal(decodeTaskPage({ tasks: [raw()], next_cursor: 'eyJ2ZXJzaW9uIjoxfQ' })?.next_cursor, 'eyJ2ZXJzaW9uIjoxfQ')
  assert.equal(decodeTaskPage({ tasks: [], next_cursor: '' })?.next_cursor, '')
  for (const value of [raw({ task_id: 9 }), raw({ status: 3 }), raw({ source_group_id: '5' }), raw({ due_at_unix_ms: '8640000000000001' })]) {
    assert.equal(decodeTask(value), null)
  }
})

test('open tasks use four due groups with inclusive seven day boundary', () => {
  const now = 1_700_000_000_000
  const task = (id: string, due: number): Task => decodeTask(raw({ task_id: id, due_at_unix_ms: String(due) }))!
  const groups = groupTasks([
    task('1', now - 1), task('2', now), task('3', now + 7 * 86400000), task('4', now + 7 * 86400000 + 1), task('5', 0),
  ], 'open', now)
  assert.deepEqual(groups.map(group => [group.key, group.tasks.map(item => item.task_id)]), [
    ['overdue', ['1', '2']], ['soon', ['3']], ['later', ['4']], ['none', ['5']],
  ])
  assert.deepEqual(groupTasks([task('6', 0)], 'completed', now).map(group => group.key), ['completed'])
})
