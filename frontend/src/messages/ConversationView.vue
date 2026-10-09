<script setup lang="ts">
import { nextTick, onUnmounted, reactive, ref, watch } from 'vue'
import { api, isId } from '../api/client.ts'
import { session } from '../auth/session.ts'
import type { Selection } from './directory.ts'
import { createHistory, initialHistoryState, type ChatMessage } from './history.ts'
import type { ConnectionState, SendStatus, TextChat } from '../realtime/client.ts'
const props = defineProps<{ conversation: Selection; joining: boolean; error: string; ownId: string; connection: ConnectionState; draft: string; outgoing: (SendStatus & { text: string })[]; notice: string; offlineNotice: string }>()
const emit = defineEmits<{ join: []; revoked: []; 'update:draft': [text: string]; send: []; retry: [msgId: string]; offline: [] }>()
const history = createHistory(api.request, session, props.ownId, reactive(initialHistoryState()))
const list = history.state
const scrollElement = ref<HTMLElement | null>(null)
const composing = ref(false)
const hasNew = ref(false)
let initialScrolled = false
const memberNames = reactive<Record<string, string>>({})
let namesScope = 0
const clearNames = session.subscribe(() => { namesScope++; for (const id of Object.keys(memberNames)) delete memberNames[id] })
async function loadMemberNames(teamId: string, epoch: number) {
  let after = '0'
  for (let page = 0; page < 5; page++) {
    try {
      const data = await api.request<{ members: { user_id: string; username: string; nickname: string }[]; next_after_user_id: string }>('/teams/' + teamId + '/members?after_user_id=' + after + '&limit=100')
      if (epoch !== namesScope) return
      if (!data || !Array.isArray(data.members) || typeof data.next_after_user_id !== 'string') return
      for (const member of data.members) {
        if (isId(member.user_id)) memberNames[member.user_id] = member.nickname?.trim() || member.username?.trim() || '成员 ' + member.user_id
      }
      const next = data.next_after_user_id
      if (next === '0' || !isId(next) || BigInt(next) <= BigInt(after)) return
      after = next
    } catch { return } // Names are optional; history still shows stable sender IDs.
  }
}
watch(() => [props.conversation.key, props.conversation.joined], () => {
  initialScrolled = false; hasNew.value = false; history.select(props.conversation)
  namesScope++
  for (const id of Object.keys(memberNames)) delete memberNames[id]
  if (props.conversation.kind === 'group' && props.conversation.joined && isId(props.conversation.teamId)) void loadMemberNames(props.conversation.teamId, namesScope)
}, { immediate: true })
watch(() => list.loaded, async loaded => {
  if (!loaded || initialScrolled) return
  initialScrolled = true
  await nextTick()
  if (scrollElement.value) scrollElement.value.scrollTop = scrollElement.value.scrollHeight
})
watch(() => list.denied, denied => { if (denied) emit('revoked') })
onUnmounted(() => { namesScope++; clearNames(); history.dispose() })
async function older() {
  const element = scrollElement.value
  const oldHeight = element?.scrollHeight ?? 0
  const oldTop = element?.scrollTop ?? 0
  await history.loadOlder()
  await nextTick()
  if (element && scrollElement.value === element) element.scrollTop = oldTop + element.scrollHeight - oldHeight
}
function atBottom() {
  const element = scrollElement.value
  return !element || element.scrollHeight - element.scrollTop - element.clientHeight < 80
}
async function applyChat(chat: TextChat) {
  return applyOffline([chat])
}
async function applyOffline(chats: TextChat[]) {
  const follow = atBottom()
  const applied = history.applyRealtimeBatch(chats)
  if (!applied) return false
  if (follow) { await nextTick(); if (scrollElement.value) scrollElement.value.scrollTop = scrollElement.value.scrollHeight }
  else hasNew.value = true
  void history.refreshUnread()
  return true
}
async function refreshFromServer() {
  await history.loadLatest()
  await history.refreshUnread(true)
  return list.messages
}
async function checkPersisted(msgId: string) {
  if (list.loading) return null
  await history.loadLatest()
  if (list.error || list.denied) return null
  return list.messages.some(item => item.msg_id === msgId && item.from_id === props.ownId)
}
function scrollToNew() {
  if (scrollElement.value) scrollElement.value.scrollTop = scrollElement.value.scrollHeight
  hasNew.value = false
}
function keydown(event: KeyboardEvent) {
  if (props.connection !== 'connected' || event.key !== 'Enter' || event.shiftKey || event.isComposing || composing.value || event.keyCode === 229) return
  event.preventDefault()
  emit('send')
}
defineExpose({ applyChat, applyOffline, refreshFromServer, checkPersisted })
function sender(message: ChatMessage) {
  if (message.sender_type === 2) return '机器人'
  if (message.from_id === props.ownId) return '我'
  if (props.conversation.kind === 'direct') return props.conversation.title
  return memberNames[message.from_id] || '成员 ' + message.from_id
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
      <span class="chat-connection" :class="connection">{{ connection === 'connected' ? '实时连接正常' : connection === 'connecting' || connection === 'reconnecting' ? '正在连接…' : '连接已断开' }}</span>
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
    <button v-if="hasNew" class="new-message-jump" type="button" @click="scrollToNew">有新消息 · 查看最新</button>
    <div v-if="(conversation.kind !== 'group' || conversation.joined) && !list.denied" class="composer-area">
      <div class="composer-surface"><textarea :value="draft" aria-label="输入消息" placeholder="输入消息，Enter 发送，Shift+Enter 换行" rows="3" @input="emit('update:draft', ($event.target as HTMLTextAreaElement).value)" @keydown="keydown" @compositionstart="composing = true" @compositionend="composing = false" /></div>
      <div class="composer-actions" style="max-width:850px;margin:auto">
        <span>阅读消息不会自动标记已读</span>
        <div>
          <button type="button" :disabled="list.unreadLoading" @click="history.refreshUnread()">{{ list.unreadLoading ? '正在核对…' : '刷新未读' }}</button>
          <button type="button" :disabled="list.marking || list.pendingConfirmation || !history.receivedIDs().length" @click="history.markLoadedRead()">{{ list.marking ? '正在确认…' : '将已加载收到的消息标为已读' }}</button>
          <button type="button" :disabled="connection !== 'connected' || !draft.trim()" @click="emit('send')">发送</button>
        </div>
      </div>
      <p v-if="list.unreadError || list.markError" class="composer-footnote" role="alert">{{ list.unreadError || list.markError }}</p>
      <p v-if="notice" class="composer-footnote" role="alert">{{ notice }}</p>
      <p v-if="offlineNotice" class="composer-footnote" role="status">{{ offlineNotice }} <button type="button" @click="emit('offline')">补拉离线消息</button></p>
      <div v-for="item in outgoing" :key="item.msgId" class="composer-footnote send-result" role="status"><span class="send-preview">{{ item.text }}</span><span>{{ item.status === 'sending' ? '正在发送…' : item.status === 'accepted' ? '服务已受理，等待历史确认' : item.status === 'confirmed' ? '消息已进入历史' : '发送结果待核对' }}</span><button v-if="item.status === 'uncertain' || item.status === 'accepted'" type="button" @click="emit('retry', item.msgId)">{{ item.status === 'uncertain' ? '核对并重试' : '核对历史' }}</button></div>
      <p class="composer-footnote">{{ connection === 'connected' ? '消息已受理不代表对方已读。' : '连接恢复后可继续发送；输入内容会保留。' }}</p>
    </div>
  </section>
</template>
