<script setup lang="ts">
import type { TaskNotification, initialNotificationState } from './notifications.ts'
defineProps<{ state: ReturnType<typeof initialNotificationState> }>()
const emit = defineEmits<{ open: [item: TaskNotification, target: HTMLElement]; more: []; refresh: []; read: [item: TaskNotification] }>()
const labels = ['待办', '进行中', '已完成']
const formatTime = (value: string) => new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'short', timeZone: 'Asia/Shanghai' }).format(new Date(Number(value)))
</script>
<template>
  <section class="task-notifications" aria-labelledby="task-notification-heading">
    <header><div><h2 id="task-notification-heading">任务动态</h2><p>未读 {{ state.unreadCount }} 条；打开任务不会自动已读。</p></div><button class="task-secondary" type="button" :disabled="state.loading" @click="emit('refresh')">刷新通知</button></header>
    <p v-if="state.error" class="task-load-error" role="alert">{{ state.error }}</p>
    <p v-if="state.loading && !state.loaded" class="task-inline-state" role="status">正在读取任务通知…</p>
    <div v-else-if="state.items.length" class="task-notification-list">
      <article v-for="item in state.items" :key="item.notification_id" class="task-notification-row" :class="{ 'is-unread': item.read_at_unix_ms === '0' }">
        <button class="task-notification-main" type="button" @click="emit('open', item, $event.currentTarget as HTMLElement)"><span><strong>{{ item.task_title }}</strong><small>{{ item.team_name || `团队 ${item.team_id}` }}</small></span><span>{{ item.actor_name || `成员 ${item.actor_id}` }}：{{ labels[item.from_status] }} → {{ labels[item.to_status] }}</span><time :datetime="new Date(Number(item.created_at_unix_ms)).toISOString()">{{ formatTime(item.created_at_unix_ms) }}</time></button>
        <div class="task-notification-action"><span>{{ item.read_at_unix_ms === '0' ? '未读' : '已读' }}</span><button v-if="item.read_at_unix_ms === '0'" type="button" :disabled="state.reading === item.notification_id" @click="emit('read', item)">{{ state.reading === item.notification_id ? '确认中…' : '标为已读' }}</button></div>
      </article>
      <button v-if="state.cursor" class="task-more" type="button" :disabled="state.loading" @click="emit('more')">加载更多通知</button>
    </div>
    <div v-else-if="state.loaded" class="task-empty"><div aria-hidden="true">✓</div><h2>暂无任务通知</h2><p>团队任务状态发生变化后会显示在这里。</p></div>
  </section>
</template>
