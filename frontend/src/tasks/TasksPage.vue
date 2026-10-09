<script setup lang="ts">
import { nextTick, onUnmounted, reactive, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, isId } from '../api/client.ts'
import { session } from '../auth/session.ts'
import TaskList from './TaskList.vue'
import TaskDetailPanel from './TaskDetailPanel.vue'
import { createTaskWorkspace, initialTaskWorkspaceState } from './workspace.ts'
import type { TaskView } from './model.ts'
const route = useRoute(), router = useRouter()
const state = reactive(initialTaskWorkspaceState())
const workspace = createTaskWorkspace(api, session, state)
let returnFocus: HTMLElement | null = null
const routeView = () => route.query.view === 'completed' ? 'completed' : 'open'
const routeTeam = () => typeof route.query.team_id === 'string' && isId(route.query.team_id) ? route.query.team_id : '0'
watch(() => [routeView(), routeTeam(), String(route.params.teamId || ''), String(route.params.taskId || '')], async ([view, teamId, detailTeam, taskId]) => {
  workspace.setView(view as TaskView); workspace.setTeam(teamId)
  if (!state.tasks.loaded && !state.tasks.loading) void workspace.loadTasks()
  await workspace.selectTask(detailTeam || undefined, taskId || undefined)
}, { immediate: true })
void workspace.loadTeams()
function changeFilter(view: TaskView, teamId: string) {
  const query: Record<string, string> = {}
  if (view === 'completed') query.view = view
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
onUnmounted(workspace.dispose)
</script>
<template>
  <main class="tasks-layout" :class="{ 'has-detail': !!route.params.taskId }">
    <section class="tasks-workspace">
      <header class="tasks-header"><div><p class="section-overline">MY WORK</p><h1>我的任务<span class="heading-period">.</span></h1><p>集中查看分配给你的团队事项</p></div><button class="task-refresh" type="button" :disabled="state.tasks.loading" @click="workspace.refreshTasks">刷新</button></header>
      <div class="task-filters" aria-label="任务筛选">
        <div class="task-view-tabs" role="group" aria-label="按状态筛选"><button type="button" :class="{ 'is-selected': state.view === 'open' }" :aria-pressed="state.view === 'open'" @click="changeFilter('open', state.teamId)">待处理</button><button type="button" :class="{ 'is-selected': state.view === 'completed' }" :aria-pressed="state.view === 'completed'" @click="changeFilter('completed', state.teamId)">已完成</button></div>
        <label>团队<select :value="state.teamId" @change="changeFilter(state.view, ($event.target as HTMLSelectElement).value)"><option value="0">全部团队</option><option v-for="team in state.teams.items" :key="team.team_id" :value="team.team_id">{{ team.name }}</option></select></label>
        <button v-if="state.teams.cursor !== '0'" class="task-team-more" type="button" :disabled="state.teams.loading" @click="workspace.loadTeams">更多团队</button>
        <span v-if="state.teams.error" class="task-team-error" role="alert">{{ state.teams.error }} <button type="button" @click="workspace.loadTeams">重试</button></span>
      </div>
      <TaskList :state="state" @open="openTask" @more="workspace.loadTasks" @retry="workspace.retryTasks" />
    </section>
    <TaskDetailPanel v-if="route.params.taskId" :detail="state.detail" :loading="state.detailLoading" :error="state.detailError" :focus-key="String(route.params.teamId) + ':' + String(route.params.taskId)" :retryable="!!state.selected" @close="closeDetail" @retry="retryDetail" />
  </main>
</template>
