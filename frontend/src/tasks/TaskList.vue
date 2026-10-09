<script setup lang="ts">
import { computed } from 'vue'
import { formatTaskDue, groupTasks, taskStatusLabel } from './model.ts'
import type { initialTaskWorkspaceState } from './workspace.ts'
const props = defineProps<{ state: ReturnType<typeof initialTaskWorkspaceState>; now?: number }>()
defineEmits<{ open: [teamId: string, taskId: string, target: HTMLElement]; more: []; retry: [] }>()
const groups = computed(() => groupTasks(props.state.tasks.items, props.state.view, props.now))
</script>
<template>
  <section class="task-list-area" aria-label="任务列表" :aria-busy="state.tasks.loading">
    <p v-if="state.tasks.loading && !state.tasks.loaded" class="task-inline-state" role="status">正在加载任务…</p>
    <div v-if="state.tasks.error" class="task-load-error" role="alert">
      <span>{{ state.tasks.error }}</span><button type="button" :disabled="state.tasks.loading" @click="$emit('retry')">重试</button>
    </div>
    <div v-if="state.tasks.loaded && !state.tasks.items.length && !state.tasks.error" class="task-empty">
      <div aria-hidden="true">✓</div><h2>当前筛选没有任务</h2><p>切换状态或团队，查看其他分配给你的任务。</p>
    </div>
    <section v-for="group in groups" :key="group.key" class="task-group" :aria-labelledby="'task-group-' + group.key">
      <header><h2 :id="'task-group-' + group.key">{{ group.label }}</h2><span>{{ group.tasks.length }}</span></header>
      <div class="task-rows">
        <button v-for="item in group.tasks" :key="item.task_id" class="task-row" :class="{ 'is-active': state.selected?.taskId === item.task_id && state.selected?.teamId === item.team_id }" type="button" :aria-current="state.selected?.taskId === item.task_id && state.selected?.teamId === item.team_id ? 'true' : undefined" @click="$emit('open', item.team_id, item.task_id, $event.currentTarget as HTMLElement)">
          <span class="task-row-main"><strong>{{ item.title }}</strong><small>{{ item.team_name }}</small></span>
          <span class="task-row-meta"><span class="task-status">{{ taskStatusLabel(item.status) }}</span><time v-if="item.due_at_unix_ms !== '0'" :datetime="new Date(Number(item.due_at_unix_ms)).toISOString()">{{ formatTaskDue(item.due_at_unix_ms) }}</time><span v-else>无截止时间</span><span v-if="item.source_message_id !== '0'" class="task-source">来自讨论</span></span>
        </button>
      </div>
    </section>
    <button v-if="state.tasks.loaded && state.tasks.cursor !== ''" class="task-more" type="button" :disabled="state.tasks.loading" @click="$emit('more')">{{ state.tasks.loading ? '正在加载…' : '加载更多任务' }}</button>
  </section>
</template>
