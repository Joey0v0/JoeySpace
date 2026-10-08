<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ConversationSummary } from './model.ts'
import { conversationPath } from './sample.ts'

const props = defineProps<{ items: readonly ConversationSummary[]; activeKey?: string }>()
const search = ref('')
const matches = computed(() => props.items.filter((item) => `${item.title} ${item.teamName ?? ''}`.toLocaleLowerCase().includes(search.value.trim().toLocaleLowerCase())))
const groups = computed(() => matches.value.filter((item) => item.kind === 'group'))
const directs = computed(() => matches.value.filter((item) => item.kind === 'direct'))
const unreadConversations = computed(() => props.items.filter((item) => item.unreadCount !== '0').length)
</script>

<template>
  <aside class="conversation-panel" aria-label="会话列表">
    <div class="conversation-panel-header">
      <div class="section-overline">WORKSPACE</div>
      <h1>消息<span class="heading-period">.</span></h1>
      <p>团队讨论与直接消息</p>
    </div>
    <div class="directory-controls">
      <RouterLink to="/messages" class="overview-entry" :class="{ 'is-active': !activeKey }" :aria-current="!activeKey ? 'page' : undefined">
        <span class="overview-entry-icon" aria-hidden="true">▦</span>
        <span class="overview-entry-text"><strong>未读总览</strong><small>优先查看需要关注的讨论</small></span>
        <span class="small-count" :aria-label="`${unreadConversations} 个未读会话`">{{ unreadConversations }}</span>
      </RouterLink>
      <div class="search-wrap">
        <span aria-hidden="true">⌕</span>
        <input v-model="search" type="search" aria-label="搜索会话" placeholder="搜索当前会话" autocomplete="off" />
      </div>
      <p class="directory-hint">仅搜索当前样例会话目录</p>
    </div>
    <div class="directory-scroll">
      <template v-if="matches.length">
        <section class="directory-section" aria-label="团队主群聊">
          <h2>团队主群聊 <span>{{ groups.length }}</span></h2>
          <div v-if="!groups.length" class="directory-empty">没有匹配的团队群聊</div>
          <RouterLink v-for="item in groups" :key="item.key" :to="conversationPath(item)" class="conversation-row" :class="{ 'is-active': activeKey === item.key }" :aria-current="activeKey === item.key ? 'page' : undefined">
            <span class="avatar avatar-group" aria-hidden="true">#</span>
            <span class="conversation-row-copy"><span class="row-title">{{ item.title }} <span v-if="item.mentioned" class="mention-mark">@我</span></span><small>{{ item.teamName }}</small></span>
            <span v-if="item.unreadCount !== '0'" class="row-unread" :aria-label="`${item.unreadCount} 条未读`">{{ item.unreadCount }}</span>
          </RouterLink>
        </section>
        <section class="directory-section" aria-label="私聊">
          <h2>私聊 <span>{{ directs.length }}</span></h2>
          <div v-if="!directs.length" class="directory-empty">没有匹配的私聊</div>
          <RouterLink v-for="item in directs" :key="item.key" :to="conversationPath(item)" class="conversation-row" :class="{ 'is-active': activeKey === item.key }" :aria-current="activeKey === item.key ? 'page' : undefined">
            <span class="avatar avatar-direct" aria-hidden="true">{{ item.title.slice(0, 1) }}</span>
            <span class="conversation-row-copy"><span class="row-title">{{ item.title }}</span><small>直接消息</small></span>
            <span v-if="item.unreadCount !== '0'" class="row-unread" :aria-label="`${item.unreadCount} 条未读`">{{ item.unreadCount }}</span>
          </RouterLink>
        </section>
      </template>
      <div v-else class="search-empty"><strong>没有匹配的会话</strong><p>试试其他名称；这里仅搜索当前样例目录。</p></div>
    </div>
    <div class="directory-footer"><span class="status-pulse" />样例目录 · 共 {{ items.length }} 个会话</div>
  </aside>
</template>
