<script setup lang="ts">
import type { Selection } from './directory.ts'
defineProps<{ conversation: Selection; joining: boolean; error: string }>()
defineEmits<{ join: [] }>()
</script>
<template>
  <section class="conversation-view">
    <header class="chat-header">
      <div class="chat-header-avatar" :class="conversation.kind" aria-hidden="true">{{ conversation.kind === 'group' ? '#' : conversation.title.slice(0, 1) }}</div>
      <div class="chat-heading"><p class="eyebrow">{{ conversation.kind === 'group' ? 'TEAM CONVERSATION' : 'DIRECT MESSAGE' }}</p><h2>{{ conversation.title }}</h2><span>{{ conversation.kind === 'group' ? '团队群聊' : '私聊' }}</span></div>
    </header>
    <div class="chat-scroll">
      <section class="unavailable-state">
        <template v-if="conversation.kind === 'group' && !conversation.joined">
          <h1>尚未加入这个讨论群</h1><p>团队成员可以发现群聊，加入后才具备群消息阅读资格。</p>
          <button class="primary-link" type="button" :disabled="joining" @click="$emit('join')">{{ joining ? '正在加入…' : '加入讨论群' }}</button>
        </template>
        <template v-else><h1>会话已找到</h1><p>消息阅读将在下一步接入。未读、@我与回复将在聊天接线后显示。</p></template>
        <p v-if="error" role="alert">{{ error }}</p>
      </section>
    </div>
  </section>
</template>
