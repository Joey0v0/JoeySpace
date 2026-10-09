<script setup lang="ts">
import { nextTick, onUnmounted, reactive, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, isId } from '../api/client.ts'
import { session } from '../auth/session.ts'
import { createRealtimeClient } from '../realtime/client.ts'
import { taskSignal } from '../realtime/taskSignal.ts'
import TaskList from './TaskList.vue'
import TaskNotifications from './TaskNotifications.vue'
import TaskDetailPanel from './TaskDetailPanel.vue'
import TaskFormPanel from './TaskFormPanel.vue'
import { createTaskWorkspace, initialTaskWorkspaceState } from './workspace.ts'
import { createStatusMutation, initialStatusMutationState } from './mutations.ts'
import type { TaskDetail, TaskStatus, TaskView } from './model.ts'
import { createTaskNotifications, decodeNotificationPage, initialNotificationState, type TaskNotification } from './notifications.ts'
const route = useRoute(), router = useRouter()
const state = reactive(initialTaskWorkspaceState())
const workspace = createTaskWorkspace(api, session, state)
const notificationState = reactive(initialNotificationState())
const notifications = createTaskNotifications(async options => {
  const query = new URLSearchParams({ limit: String(options.limit) })
  if (options.teamId !== '0') query.set('team_id', options.teamId)
  if (options.cursor) query.set('cursor', options.cursor)
  const page = decodeNotificationPage(await api.request('/task-notifications?' + query))
  if (!page) throw new Error('invalid notification page')
  return page
}, async item => api.request(`/teams/${item.team_id}/task-notifications/${item.notification_id}/read`, { method: 'PUT' }) as Promise<{ notification_id: string; read_at_unix_ms: string }>, session, notificationState)
const mutationState = reactive(initialStatusMutationState())
const mutation = createStatusMutation(api.request, api.getTask, session, mutationState, {
  apply(detail) { if (state.selected?.teamId === detail.task.team_id && state.selected.taskId === detail.task.task_id) state.detail = detail },
  clear() { state.detail = null; state.detailError = '任务不存在或当前账号无法访问' },
  refresh: workspace.refreshTasks,
})
let returnFocus: HTMLElement | null = null
const routeView = () => route.query.view === 'completed' ? 'completed' : 'open'
const notificationTab = () => route.query.tab === 'notifications'
const routeTeam = () => typeof route.query.team_id === 'string' && isId(route.query.team_id) ? route.query.team_id : '0'
watch(() => [routeView(), routeTeam(), String(route.params.teamId || ''), String(route.params.taskId || ''), String(route.name || ''), notificationTab() ? 'notifications' : 'tasks'], async ([view, teamId, detailTeam, taskId, , tab]) => {
  mutation.reset()
  workspace.setView(view as TaskView); workspace.setTeam(teamId)
  if (tab === 'notifications') { notifications.setTeam(teamId); if (!notificationState.loaded && !notificationState.loading) void notifications.load() }
  else if (!state.tasks.loaded && !state.tasks.loading) void workspace.loadTasks()
  await workspace.selectTask(detailTeam || undefined, taskId || undefined)
}, { immediate: true })
void workspace.loadTeams()
function changeTab(tab: 'open' | 'completed' | 'notifications', teamId: string) {
  const query: Record<string, string> = {}
  if (tab === 'completed') query.view = 'completed'
  if (tab === 'notifications') query.tab = 'notifications'
  if (teamId !== '0') query.team_id = teamId
  void router.push({ path: '/tasks', query })
}
function openTask(teamId: string, taskId: string, target: HTMLElement) {
  returnFocus = target
  void router.push({ path: `/tasks/teams/${teamId}/${taskId}`, query: route.query })
}
async function closeDetail() {
  await router.push({ path: '/tasks', query: route.query })
  await nextTick(); returnFocus?.focus(); returnFocus = null
}
function retryDetail() { if (state.selected) void workspace.selectTask(state.selected.teamId, state.selected.taskId) }
function openCreate() {
  const query: Record<string, string> = {}
  if (state.teamId !== '0') query.team_id = state.teamId
  void router.push({ path: '/tasks/new', query })
}
function closeCreate() { router.back() }
async function created(detail: TaskDetail) {
  await workspace.refreshTasks()
  await router.replace(`/tasks/teams/${detail.task.team_id}/${detail.task.task_id}`)
}
function updateStatus(status: TaskStatus) { if (state.detail) void mutation.update(state.detail, status) }
const sourceParts = () => ({
  team: typeof route.query.team_id === 'string' ? route.query.team_id : '',
  group: typeof route.query.source_group_id === 'string' ? route.query.source_group_id : '',
  message: typeof route.query.source_message_id === 'string' ? route.query.source_message_id : '',
})
const sourceInvalid = () => {
  const value = sourceParts(), any = !!(value.group || value.message)
  return any && !(isId(value.team) && isId(value.group) && isId(value.message))
}
const realtime = createRealtimeClient({ identity: session, onTaskNotification: hint => { taskSignal.set(); if (notificationTab()) notifications.hint(hint.notificationId) }, onRefresh: () => { taskSignal.set(); if (notificationTab()) notifications.hint() } })
realtime.connect()
function openNotification(item: TaskNotification, target: HTMLElement) { openTask(item.team_id, item.task_id, target) }
onUnmounted(() => { realtime.dispose(); notifications.dispose(); mutation.dispose(); workspace.dispose() })
</script>
<template>
  <main class="tasks-layout" :class="{ 'has-detail': !!route.params.taskId || route.name === 'task-new' }">
    <section class="tasks-workspace">
      <header class="tasks-header"><div><p class="section-overline">MY WORK</p><h1>我的任务<span class="heading-period">.</span></h1><p>集中查看分配给你的团队事项</p></div><div class="task-header-actions"><button class="primary-link" type="button" @click="openCreate">新建任务</button><button class="task-refresh" type="button" :disabled="state.tasks.loading" @click="workspace.refreshTasks">刷新</button></div></header>
      <div class="task-filters" aria-label="任务筛选">
        <div class="task-view-tabs" role="group" aria-label="任务页面"><button type="button" :class="{ 'is-selected': !notificationTab() && state.view === 'open' }" :aria-pressed="!notificationTab() && state.view === 'open'" @click="changeTab('open', state.teamId)">待处理</button><button type="button" :class="{ 'is-selected': !notificationTab() && state.view === 'completed' }" :aria-pressed="!notificationTab() && state.view === 'completed'" @click="changeTab('completed', state.teamId)">已完成</button><button type="button" :class="{ 'is-selected': notificationTab() }" :aria-pressed="notificationTab()" @click="changeTab('notifications', state.teamId)">通知</button></div>
        <label>团队<select :value="state.teamId" @change="changeTab(notificationTab() ? 'notifications' : state.view, ($event.target as HTMLSelectElement).value)"><option value="0">全部团队</option><option v-for="team in state.teams.items" :key="team.team_id" :value="team.team_id">{{ team.name }}</option></select></label>
        <button v-if="state.teams.cursor !== '0'" class="task-team-more" type="button" :disabled="state.teams.loading" @click="workspace.loadTeams">更多团队</button>
        <span v-if="state.teams.error" class="task-team-error" role="alert">{{ state.teams.error }} <button type="button" @click="workspace.loadTeams">重试</button></span>
      </div>
      <TaskNotifications v-if="notificationTab()" :state="notificationState" @open="openNotification" @more="notifications.loadMore" @refresh="notifications.refresh" @read="notifications.markRead" />
      <TaskList v-else :state="state" @open="openTask" @more="workspace.loadTasks" @retry="workspace.retryTasks" />
    </section>
    <TaskFormPanel v-if="route.name === 'task-new'" :key="route.fullPath" :teams="state.teams.items" :default-team-id="sourceParts().team || (state.teamId !== '0' ? state.teamId : '')" :source-group-id="sourceParts().group" :source-message-id="sourceParts().message" :invalid-source="sourceInvalid()" @close="closeCreate" @created="created" />
    <TaskDetailPanel v-else-if="route.params.taskId" :detail="state.detail" :loading="state.detailLoading" :error="state.detailError" :focus-key="String(route.params.teamId) + ':' + String(route.params.taskId)" :retryable="!!state.selected" :mutation="mutationState" @close="closeDetail" @retry="retryDetail" @status="updateStatus" @mutation-retry="mutation.retry" @mutation-recheck="mutation.recheck" />
  </main>
</template>
