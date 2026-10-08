<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ConversationSummary } from './model.ts'
import { getUnreadConversations } from './model.ts'
import { conversationPath } from './sample.ts'

const props = defineProps<{ items: readonly ConversationSummary[] }>()
const filter = ref<'all' | 'mentions'>('all')
const unread = computed(() => getUnreadConversations(props.items, filter.value))
const allCount = computed(() => getUnreadConversations(props.items, 'all').length)
const mentionCount = computed(() => getUnreadConversations(props.items, 'mentions').length)
const timeText = (value: number) => new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false }).format(value)
</script>

<template>
  <section class="overview-page">
    <header class="overview-header">
      <div>
        <p class="eyebrow">YOUR INBOX / 未读总览</p>
        <h2>先看重要的讨论<span class="heading-period">.</span></h2>
        <p class="overview-intro">团队群聊和私聊汇在这里。每张卡片对应一个会话，方便从讨论继续工作。</p>
      </div>
      <div class="overview-total"><span>{{ allCount.toString().padStart(2, '0') }}</span><small>个未读会话<br />样例数据</small></div>
    </header>
    <div class="overview-content">
      <div class="section-heading"><div><p class="eyebrow">FOCUS</p><h3>需要你关注</h3></div><span class="section-rule" /></div>
      <div class="filter-tabs" role="group" aria-label="未读筛选">
        <button type="button" :class="{ 'is-selected': filter === 'all' }" :aria-pressed="filter === 'all'" @click="filter = 'all'">全部未读 <span>{{ allCount }}</span></button>
        <button type="button" :class="{ 'is-selected': filter === 'mentions' }" :aria-pressed="filter === 'mentions'" @click="filter = 'mentions'">@我 <span>{{ mentionCount }}</span></button>
      </div>
      <div v-if="unread.length" class="unread-grid">
        <RouterLink v-for="item in unread" :key="item.key" :to="conversationPath(item)" class="unread-card">
          <span class="unread-card-icon" :class="item.kind" aria-hidden="true">{{ item.kind === 'group' ? '#' : item.title.slice(0, 1) }}</span>
          <span class="unread-card-body">
            <span class="card-meta"><span>{{ item.kind === 'group' ? '团队群聊' : '私聊' }}<template v-if="item.teamName"> · {{ item.teamName }}</template></span><time>{{ timeText(item.updatedAt) }}</time></span>
            <span class="card-title">{{ item.title }} <span v-if="item.mentioned" class="mention-mark">@我</span></span>
            <span class="card-preview">{{ item.preview }}</span>
            <span class="card-action">查看讨论 <span aria-hidden="true">↗</span></span>
          </span>
          <span class="card-unread">{{ item.unreadCount }} 条未读</span>
        </RouterLink>
      </div>
      <div v-else class="overview-empty">
        <div class="empty-symbol" aria-hidden="true">○</div>
        <h3>{{ filter === 'mentions' ? '暂时没有 @我的未读会话' : '暂时没有未读消息' }}</h3>
        <p>可以从左侧会话列表继续查看讨论。这里展示的是固定样例，不代表真实阅读状态。</p>
      </div>
    </div>
  </section>
</template>
