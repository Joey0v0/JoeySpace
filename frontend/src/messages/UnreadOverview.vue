<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive } from 'vue'
import { api } from '../api/client.ts'
import { session } from '../auth/session.ts'
import { createUnreadOverview, initialUnreadOverviewState, unreadConversationPath, type UnreadConversation } from './unreadOverview.ts'

const state = reactive(initialUnreadOverviewState())
const overview = createUnreadOverview(api.request, session, state)
const unreadTotal = computed(() => state.items.reduce((sum, item) => sum + BigInt(state.filter === 'mentions' ? item.mention_unread_count : item.unread_count), 0n).toString())
const hasMore = computed(() => state.loaded && state.cursor !== '0')
function title(item: UnreadConversation) {
  return item.chat_type === 1 ? item.display_name || `用户 ${item.peer_id}` : item.group_name || `群聊 ${item.group_id}`
}
function timeLabel(item: UnreadConversation) {
  return new Date(item.last_message_time_unix_ms).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}
onMounted(() => { void overview.load() })
onUnmounted(() => overview.dispose())
</script>
<template>
  <section class="overview-page">
    <header class="overview-header">
      <div><p class="eyebrow">YOUR WORKSPACE / 未读总览</p><h2>先看需要回复的讨论<span class="heading-period">.</span></h2><p class="overview-intro">群聊和私聊按最近消息排在一起。进入会话后，仍会按当前账号复核访问权限；只有显式标记已读才会减少未读数。</p></div>
      <div v-if="state.loaded" class="overview-total"><span>{{ unreadTotal }}</span><small>已加载消息<br />未读条数</small></div>
    </header>
    <div class="overview-content">
      <div class="section-heading"><div><p class="eyebrow">UNREAD</p><h3>{{ state.filter === 'mentions' ? '@我的未读' : '全部未读' }}</h3></div><span class="section-rule" /></div>
      <div class="filter-tabs"><button :class="{ 'is-selected': state.filter === 'all' }" type="button" :aria-current="state.filter === 'all' ? 'true' : undefined" @click="overview.setFilter('all')">全部未读</button><button :class="{ 'is-selected': state.filter === 'mentions' }" type="button" :aria-current="state.filter === 'mentions' ? 'true' : undefined" @click="overview.setFilter('mentions')">@我</button><button type="button" :disabled="state.loading" @click="overview.load(true)">刷新</button></div>
      <p v-if="state.loading && !state.loaded" role="status">正在读取未读会话…</p>
      <p v-if="state.error" role="alert">{{ state.error }} <button type="button" @click="overview.retry()">重试</button></p>
      <div v-if="state.items.length" class="unread-grid">
        <RouterLink v-for="item in state.items" :key="`${item.chat_type}:${item.team_id}:${item.group_id}:${item.peer_id}`" class="unread-card" :to="unreadConversationPath(item)">
          <span class="unread-card-icon" :class="{ direct: item.chat_type === 1 }" aria-hidden="true">{{ item.chat_type === 1 ? '✉' : '#' }}</span>
          <span class="unread-card-body"><span class="card-meta"><span>{{ item.chat_type === 1 ? '私聊' : '团队群聊' }}</span><time :datetime="new Date(item.last_message_time_unix_ms).toISOString()">{{ timeLabel(item) }}</time></span><strong class="card-title">{{ title(item) }}</strong><span class="card-preview">{{ item.preview || '最近一条消息' }}</span><span class="card-action">查看讨论 <span aria-hidden="true">→</span></span></span>
          <span class="card-unread">{{ state.filter === 'mentions' ? item.mention_unread_count + ' 条提及未读' : item.unread_count + ' 条未读' }}</span>
        </RouterLink>
      </div>
      <div v-else-if="state.loaded && !state.loading && !state.error" class="overview-empty"><div class="empty-symbol" aria-hidden="true">✓</div><h3>{{ state.filter === 'mentions' ? '目前没有提及你的未读消息' : '目前没有未读会话' }}</h3><p>可从左侧进入团队或私聊；新消息到达后刷新这里查看。</p><RouterLink class="primary-link" to="/tasks">前往我的任务</RouterLink></div>
      <button v-if="hasMore" class="primary-link" type="button" :disabled="state.loading" @click="overview.load()">{{ state.loading ? '正在加载…' : '加载更多未读会话' }}</button>
      <p v-if="state.loaded && !hasMore && state.items.length" role="status">已显示当前快照中的全部未读会话；新消息请点击刷新。</p>
    </div>
  </section>
</template>
