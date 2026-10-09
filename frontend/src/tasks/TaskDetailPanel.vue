<script setup lang="ts">
import { nextTick, onMounted, ref, watch } from 'vue'
import { formatTaskDue, taskStatusLabel, type TaskDetail } from './model.ts'
const props = defineProps<{ detail: TaskDetail | null; loading: boolean; error: string; focusKey: string; retryable: boolean }>()
defineEmits<{ close: []; retry: [] }>()
const closeButton = ref<HTMLButtonElement | null>(null)
async function focusClose() { await nextTick(); closeButton.value?.focus() }
onMounted(focusClose)
watch(() => props.focusKey, focusClose)
</script>
<template>
  <aside class="task-detail-panel" aria-label="任务详情" :aria-busy="loading">
    <header class="task-detail-header"><div><p>任务详情</p><h2 v-if="detail">{{ detail.task.title }}</h2><h2 v-else>{{ loading ? '正在检查任务…' : '无法打开任务' }}</h2></div><button ref="closeButton" type="button" aria-label="关闭任务详情" @click="$emit('close')">×</button></header>
    <div v-if="loading" class="task-detail-state" role="status">正在按当前身份读取任务…</div>
    <div v-else-if="!detail" class="task-detail-state"><p role="alert">{{ error || '任务不存在或当前账号无法访问' }}</p><button v-if="retryable" type="button" class="task-secondary" @click="$emit('retry')">重新检查</button></div>
    <div v-else class="task-detail-body">
      <section><h3>说明</h3><p class="task-description">{{ detail.task.description || '暂无说明' }}</p></section>
      <dl>
        <div><dt>团队</dt><dd>{{ detail.task.team_name }}</dd></div>
        <div><dt>状态</dt><dd>{{ taskStatusLabel(detail.task.status) }}</dd></div>
        <div><dt>截止时间</dt><dd><time v-if="detail.task.due_at_unix_ms !== '0'" :datetime="new Date(Number(detail.task.due_at_unix_ms)).toISOString()">{{ formatTaskDue(detail.task.due_at_unix_ms) }}</time><span v-else>无截止时间</span></dd></div>
        <div><dt>创建人</dt><dd>{{ detail.task.creator_name }}</dd></div>
        <div><dt>负责人</dt><dd>{{ detail.task.assignee_id === '0' ? '未分配' : detail.task.assignee_name }}</dd></div>
      </dl>
      <section v-if="detail.task.source_message_id !== '0'" class="task-source-detail"><h3>讨论来源</h3><p>群聊 {{ detail.task.source_group_id }} · 消息 {{ detail.task.source_message_id }}</p></section>
    </div>
  </aside>
</template>
