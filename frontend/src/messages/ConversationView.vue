<script setup lang="ts">
import type { ConversationSummary, MessagePreview } from './model.ts'

defineProps<{ conversation: ConversationSummary; messages: readonly MessagePreview[] }>()
</script>

<template>
  <section class="conversation-view">
    <header class="chat-header">
      <div class="chat-header-avatar" :class="conversation.kind" aria-hidden="true">{{ conversation.kind === 'group' ? '#' : conversation.title.slice(0, 1) }}</div>
      <div class="chat-heading"><p class="eyebrow">{{ conversation.kind === 'group' ? 'TEAM CONVERSATION' : 'DIRECT MESSAGE' }}</p><h2>{{ conversation.title }}</h2><span>{{ conversation.kind === 'group' ? `${conversation.teamName} · 团队主群聊` : '私聊' }}</span></div>
      <div class="chat-unread">{{ conversation.unreadCount === '0' ? '暂无未读' : `${conversation.unreadCount} 条未读` }} · 样例</div>
    </header>
    <div class="chat-scroll">
      <div class="day-divider"><span>今天</span></div>
      <div class="chat-messages">
        <article v-for="message in messages" :key="message.id" class="chat-message" :class="{ 'is-own': message.own }">
          <div class="message-avatar" :class="{ 'is-bot': message.bot }" aria-hidden="true">{{ message.bot ? '✦' : message.senderName.slice(0, 1) }}</div>
          <div class="message-content"><div class="message-byline"><strong>{{ message.senderName }}</strong><span v-if="message.bot" class="bot-badge">机器人</span><time>{{ message.timeLabel }}</time></div><p>{{ message.content }}</p></div>
        </article>
      </div>
    </div>
    <div class="composer-area">
      <div class="composer-surface"><textarea aria-label="消息输入（样例中不可用）" placeholder="后续接入真实消息服务后可在这里回复" rows="2" disabled /><div class="composer-actions"><span>样例预览 · 暂不能发送消息</span><button type="button" disabled>发送消息 ↗</button></div></div>
      <p class="composer-footnote">查看样例会话不会清除未读数；已读确认将在真实接口接入时实现。</p>
    </div>
  </section>
</template>
