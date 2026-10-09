<script setup lang="ts">
import { nextTick, onUnmounted, reactive, ref, watch } from 'vue'
import { api } from '../api/client.ts'
import { session } from '../auth/session.ts'
import type { Selection } from './directory.ts'
import { createHistory, initialHistoryState, type ChatMessage } from './history.ts'
const props = defineProps<{ conversation: Selection; joining: boolean; error: string; ownId: string }>()
const emit = defineEmits<{ join: []; revoked: [] }>()
const history = createHistory(api.request, session, props.ownId, reactive(initialHistoryState()))
const list = history.state
const scrollElement = ref<HTMLElement | null>(null)
watch(() => [props.conversation.key, props.conversation.joined], () => history.select(props.conversation), { immediate: true })
watch(() => list.denied, denied => { if (denied) emit('revoked') })
onUnmounted(history.dispose)
async function older() {
  const element = scrollElement.value
  const oldHeight = element?.scrollHeight ?? 0
  const oldTop = element?.scrollTop ?? 0
  await history.loadOlder()
  await nextTick()
  if (element && scrollElement.value === element) element.scrollTop = oldTop + element.scrollHeight - oldHeight
}
function sender(message: ChatMessage) {
  if (message.sender_type === 2) return '机器人'
  if (message.from_id === props.ownId) return '我'
  if (props.conversation.kind === 'direct') return props.conversation.title
  return '成员 ' + message.from_id
}
function displayContent(message: ChatMessage) { return message.content_type === 1 ? message.content : '[其他类型消息]' }
function displayTime(ms: number) { return new Date(ms).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) }
</script>
<template>
  <section class="conversation-view">
    <header class="chat-header">
      <div class="chat-header-avatar" :class="conversation.kind" aria-hidden="true">{{ conversation.kind === 'group' ? '#' : conversation.title.slice(0, 1) }}</div>
      <div class="chat-heading"><p class="eyebrow">{{ conversation.kind === 'group' ? 'TEAM CONVERSATION' : 'DIRECT MESSAGE' }}</p><h2>{{ conversation.title }}</h2><span>{{ conversation.kind === 'group' ? '团队群聊' : '私聊' }}</span></div>
      <span v-if="list.unreadCount !== ''" class="chat-unread">当前未读 {{ list.unreadCount }}</span>
    </header>
    <div ref="scrollElement" class="chat-scroll">
      <section v-if="conversation.kind === 'group' && !conversation.joined" class="unavailable-state">
        <h1>尚未加入这个讨论群</h1><p>团队成员可以发现群聊，加入后才具备群消息阅读资格。</p>
        <button class="primary-link" type="button" :disabled="joining" @click="$emit('join')">{{ joining ? '正在加入…' : '加入讨论群' }}</button>
        <p v-if="error" role="alert">{{ error }}</p>
      </section>
      <section v-else-if="list.denied" class="unavailable-state" role="alert"><h1>会话访问已失效</h1><p>{{ list.error }}</p></section>
      <template v-else>
        <div v-if="list.loaded && list.cursor !== '0'" style="text-align:center;margin-bottom:20px"><button type="button" :disabled="list.loading" @click="older">{{ list.loading ? '正在加载…' : '加载更早消息' }}</button></div>
        <div v-if="list.loading && !list.loaded" role="status" class="unavailable-state">正在读取消息…</div>
        <div v-if="list.error" role="alert" class="unavailable-state"><p>{{ list.error }}</p><button type="button" @click="list.loaded ? older() : history.loadLatest()">重试读取</button></div>
        <div v-if="list.loaded && !list.messages.length" class="unavailable-state">这个会话还没有消息。</div>
        <div v-if="list.loaded && list.messages.length" class="chat-messages">
          <article v-for="item in list.messages" :key="item.id" class="chat-message" :class="{ 'is-own': item.from_id === ownId && item.sender_type !== 2 }">
            <div class="message-avatar" :class="{ 'is-bot': item.sender_type === 2 }" aria-hidden="true">{{ item.sender_type === 2 ? '✦' : sender(item).slice(0, 1) }}</div>
            <div class="message-content"><div class="message-byline"><strong>{{ sender(item) }}</strong><span v-if="item.sender_type === 2" class="bot-badge">BOT</span><time :datetime="new Date(item.created_at_unix_ms).toISOString()">{{ displayTime(item.created_at_unix_ms) }}</time></div><p>{{ displayContent(item) }}</p></div>
          </article>
        </div>
      </template>
    </div>
    <div v-if="(conversation.kind !== 'group' || conversation.joined) && !list.denied" class="composer-area">
      <div class="composer-actions" style="max-width:850px;margin:auto">
        <span>阅读消息不会自动标记已读</span>
        <div>
          <button type="button" :disabled="list.unreadLoading" @click="history.refreshUnread()">{{ list.unreadLoading ? '正在核对…' : '刷新未读' }}</button>
          <button type="button" :disabled="list.marking || list.pendingConfirmation || !history.receivedIDs().length" @click="history.markLoadedRead()">{{ list.marking ? '正在确认…' : '将已加载收到的消息标为已读' }}</button>
        </div>
      </div>
      <p v-if="list.unreadError || list.markError" class="composer-footnote" role="alert">{{ list.unreadError || list.markError }}</p>
      <p class="composer-footnote">发送与实时更新将在下一步接入。</p>
    </div>
  </section>
</template>
