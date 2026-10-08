<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import ConversationList from './ConversationList.vue'
import ConversationView from './ConversationView.vue'
import UnreadOverview from './UnreadOverview.vue'
import { findConversation } from './model.ts'
import { conversations, messages } from './sample.ts'

const route = useRoute()
const key = computed(() => {
  if (route.name === 'direct') return `direct:${route.params.peerId}`
  if (route.name === 'group') return `group:${route.params.teamId}:${route.params.groupId}`
  return undefined
})
const current = computed(() => key.value ? findConversation(conversations, key.value) : undefined)
</script>

<template>
  <main class="messages-layout">
    <ConversationList :items="conversations" :active-key="key" />
    <div class="messages-workspace">
      <div class="sample-ribbon" role="note"><span class="sample-dot" />界面样例 · 未连接真实账号或消息服务</div>
      <UnreadOverview v-if="!key" :items="conversations" />
      <ConversationView v-else-if="current" :conversation="current" :messages="messages[current.key] ?? []" />
      <section v-else class="unavailable-state">
        <div class="empty-symbol" aria-hidden="true">?</div>
        <p class="eyebrow">会话不可用</p>
        <h1>当前会话不可用</h1>
        <p>这个地址不在当前样例会话目录中。请从左侧选择会话，或返回未读总览。</p>
        <RouterLink class="primary-link" to="/messages">返回未读总览</RouterLink>
      </section>
    </div>
  </main>
</template>
