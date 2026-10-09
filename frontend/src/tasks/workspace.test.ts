import test from 'node:test'
import assert from 'node:assert/strict'
import { ApiError } from '../api/client.ts'
import { createSession } from '../auth/session.ts'
import { createTaskWorkspace, initialTaskWorkspaceState } from './workspace.ts'
import type { Task, TaskDetail, TaskPage } from './model.ts'

const task = (id: string, teamId = '2'): Task => ({ task_id: id, team_id: teamId, team_name: '研发', title: '任务' + id, description: '', creator_id: '3', creator_name: '甲', assignee_id: '4', assignee_name: '乙', status: 0, source_group_id: '0', source_message_id: '0', due_at_unix_ms: '0' })
const deferred = <T>() => { let resolve!: (value: T) => void; let reject!: (reason: unknown) => void; return { promise: new Promise<T>((a, b) => { resolve = a; reject = b }), resolve, reject } }

test('task pagination merges unique rows and failure preserves rows and cursor', async () => {
  let page = 0
  const cursors: string[] = []
  const api = {
    listMyTasks: async (options: { cursor?: string }): Promise<TaskPage> => {
      cursors.push(options.cursor ?? '')
      return ++page === 1 ? { tasks: [task('1')], next_cursor: 'cursor_one' } : page === 2 ? { tasks: [task('1'), task('2')], next_cursor: 'cursor_two' } : page === 3 ? Promise.reject(new ApiError(503, '稍后重试')) : { tasks: [task('3')], next_cursor: '' }
    },
    getTask: async (): Promise<TaskDetail> => ({ task: task('1'), can_update_status: true }), request: async () => ({}),
  }
  const state = initialTaskWorkspaceState(), workspace = createTaskWorkspace(api, createSession(), state)
  await workspace.loadTasks(); await workspace.loadTasks()
  assert.deepEqual(state.tasks.items.map(item => item.task_id), ['1', '2']); assert.equal(state.tasks.cursor, 'cursor_two')
  await workspace.loadTasks()
  assert.deepEqual(state.tasks.items.map(item => item.task_id), ['1', '2']); assert.equal(state.tasks.cursor, 'cursor_two'); assert.match(state.tasks.error, /稍后/)
  assert.equal(state.taskRetry, 'more')
  await workspace.retryTasks()
  assert.deepEqual(cursors, ['', 'cursor_one', 'cursor_two', 'cursor_two'])
  assert.deepEqual(state.tasks.items.map(item => item.task_id), ['1', '2', '3']); assert.equal(state.tasks.cursor, '')
  workspace.dispose()
})

test('refresh failure retains same-filter rows while filter switch clears them', async () => {
  let fail = false
  const cursors: string[] = []
  const api = { listMyTasks: async (options: { cursor?: string }) => { cursors.push(options.cursor ?? ''); return fail ? Promise.reject(new ApiError(503, '失败')) : ({ tasks: [task('1')], next_cursor: '' }) }, getTask: async () => ({ task: task('1'), can_update_status: true }), request: async () => ({}) }
  const state = initialTaskWorkspaceState(), workspace = createTaskWorkspace(api, createSession(), state)
  await workspace.loadTasks(); fail = true; await workspace.refreshTasks()
  assert.equal(state.tasks.items.length, 1); assert.equal(state.taskRetry, 'refresh')
  fail = false; await workspace.retryTasks()
  assert.deepEqual(cursors, ['', '', '']); assert.equal(state.taskRetry, null)
  workspace.setView('completed')
  assert.equal(state.tasks.items.length, 0); assert.equal(state.taskRetry, null)
  workspace.dispose()
})

test('failed initial task page retries from the empty cursor', async () => {
  const cursors: string[] = []
  const api = { listMyTasks: async (options: { cursor?: string }) => { cursors.push(options.cursor ?? ''); if (cursors.length === 1) throw new ApiError(503, '失败'); return { tasks: [task('1')], next_cursor: '' } }, getTask: async () => ({ task: task('1'), can_update_status: true }), request: async () => ({}) }
  const state = initialTaskWorkspaceState(), workspace = createTaskWorkspace(api, createSession(), state)
  await workspace.loadTasks(); assert.equal(state.taskRetry, 'initial')
  await workspace.retryTasks()
  assert.deepEqual(cursors, ['', '']); assert.equal(state.tasks.items[0]?.task_id, '1')
  workspace.dispose()
})

test('stale task and detail responses cannot replace a newer scope', async () => {
  const oldPage = deferred<TaskPage>(), newPage = deferred<TaskPage>(), oldDetail = deferred<TaskDetail>(), newDetail = deferred<TaskDetail>()
  let listCalls = 0, detailCalls = 0
  const api = { listMyTasks: () => ++listCalls === 1 ? oldPage.promise : newPage.promise, getTask: () => ++detailCalls === 1 ? oldDetail.promise : newDetail.promise, request: async () => ({}) }
  const state = initialTaskWorkspaceState(), workspace = createTaskWorkspace(api, createSession(), state)
  const first = workspace.loadTasks(); workspace.setView('completed'); const second = workspace.loadTasks()
  newPage.resolve({ tasks: [task('2')], next_cursor: '' }); await second; oldPage.resolve({ tasks: [task('1')], next_cursor: '' }); await first
  assert.deepEqual(state.tasks.items.map(item => item.task_id), ['2'])
  const a = workspace.selectTask('2', '2'); const b = workspace.selectTask('2', '3')
  newDetail.resolve({ task: task('3'), can_update_status: false }); await b; oldDetail.resolve({ task: task('2'), can_update_status: true }); await a
  assert.equal(state.detail?.task.task_id, '3')
  workspace.dispose()
})

test('denied detail clears private detail, account switch clears all task state', async () => {
  const identity = createSession(); identity.setSession('a')
  let denied = false
  const api = { listMyTasks: async () => ({ tasks: [task('1')], next_cursor: '' }), getTask: async () => denied ? Promise.reject(new ApiError(403, '无权访问')) : ({ task: task('1'), can_update_status: true }), request: async () => ({}) }
  const state = initialTaskWorkspaceState(), workspace = createTaskWorkspace(api, identity, state)
  await workspace.loadTasks(); await workspace.selectTask('2', '1'); denied = true; await workspace.selectTask('2', '1')
  assert.equal(state.detail, null); assert.equal(state.tasks.items.length, 1)
  identity.setSession('b')
  assert.equal(state.tasks.items.length, 0); assert.equal(state.detail, null)
  workspace.dispose()
})

test('team directory paginates and keeps options after a continuation failure', async () => {
  let calls = 0
  const api = { listMyTasks: async () => ({ tasks: [], next_cursor: '' }), getTask: async () => ({ task: task('1'), can_update_status: true }), request: async () => ++calls === 1 ? ({ teams: [{ team_id: '2', name: '研发', role: 1 }], next_after_team_id: '2' }) : Promise.reject(new ApiError(503, '失败')) }
  const state = initialTaskWorkspaceState(), workspace = createTaskWorkspace(api, createSession(), state)
  await workspace.loadTeams(); await workspace.loadTeams()
  assert.deepEqual(state.teams.items.map(item => item.team_id), ['2']); assert.equal(state.teams.cursor, '2'); assert.ok(state.teams.error)
  workspace.dispose()
})
