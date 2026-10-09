<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import ConversationList from './ConversationList.vue'
import ConversationView from './ConversationView.vue'
import UnreadOverview from './UnreadOverview.vue'
import { createDirectory, initialDirectoryState } from './directory.ts'
import { api, currentProfile, isId } from '../api/client.ts'
import { session } from '../auth/session.ts'
import { createRealtimeClient, type ConnectionState, type SendStatus, type TextChat } from '../realtime/client.ts'
import type { ChatMessage } from './history.ts'
import { createOfflineInbox, type OfflineMessage } from './offline.ts'
const route = useRoute()
const state = reactive(initialDirectoryState())
const directory = createDirectory(api.request, session, state)
interface MentionTarget { id: string; name: string }
interface Outgoing extends SendStatus { text: string; toId: string; chatType: 1 | 2; mentionedUserIds: string[]; sentAt: number }
interface ConversationHandle { applyChat(chat: TextChat): Promise<boolean>; applyOffline(chats: TextChat[]): Promise<boolean>; refreshFromServer(): Promise<ChatMessage[]>; checkPersisted(msgId: string): Promise<boolean | null> }
const view = ref<ConversationHandle | null>(null)
const connection = ref<ConnectionState>('idle')
const drafts = reactive<Record<string, string>>({})
const mentions = reactive<Record<string, MentionTarget[]>>({})
const outgoing = reactive<Record<string, Outgoing[]>>({})
const notices = reactive<Record<string, string>>({})
const offlineNotice = ref('')
const offlineCache = new Map<string, OfflineMessage>()
const key = computed(() => {
  if (route.name === 'direct') return 'direct:' + String(route.params.peerId)
  if (route.name === 'group') return 'group:' + String(route.params.teamId) + ':' + String(route.params.groupId)
  return undefined
})
watch(key, value => { void directory.select(value) }, { immediate: true })
const currentDraft = computed(() => key.value ? drafts[key.value] ?? '' : '')
const currentMentions = computed(() => key.value ? mentions[key.value] ?? [] : [])
const currentOutgoing = computed(() => key.value ? outgoing[key.value] ?? [] : [])
const currentNotice = computed(() => key.value ? notices[key.value] ?? '' : '')
const clearLocal = session.subscribe(() => {
  for (const value of [drafts, mentions, outgoing, notices]) for (const name of Object.keys(value)) delete value[name]
  offlineCache.clear()
  offlineNotice.value = ''
})
function locate(msgId: string): Outgoing | undefined {
  for (const entries of Object.values(outgoing)) {
    const item = entries.find(entry => entry.msgId === msgId)
    if (item) return item
  }
}
async function reconcile() {
  const selectedKey = key.value, epoch = session.version()
  if (!selectedKey || !view.value) return
  const messages = await view.value.refreshFromServer()
  if (selectedKey !== key.value || epoch !== session.version()) return
  for (const item of outgoing[selectedKey] ?? []) {
    if (item.status !== 'confirmed' && messages.some(message => message.msg_id === item.msgId && message.from_id === currentProfile?.id)) realtime.confirmPersisted(item.msgId)
  }
}
const realtime = createRealtimeClient({
  identity: session,
  onState: state => { connection.value = state },
  onChat: chat => {
    if (chat.fromId === currentProfile?.id && locate(chat.msgId)) realtime.confirmPersisted(chat.msgId)
    void view.value?.applyChat(chat)
  },
  onSendStatus: status => {
    const item = locate(status.msgId)
    if (!item) return
    item.status = status.status
    item.error = status.error
    if (status.status === 'confirmed') {
      setTimeout(() => {
        if (item.status !== 'confirmed') return
        for (const entries of Object.values(outgoing)) {
          const index = entries.indexOf(item)
          if (index >= 0) { entries.splice(index, 1); break }
        }
      }, 4000)
    }
    if (status.status === 'accepted') {
      setTimeout(() => { void reconcile() }, 700)
      setTimeout(() => { void reconcile() }, 2500)
    }
  },
  onRefresh: () => { void reconcile(); void pullOffline() },
})
const offline = createOfflineInbox(api.request, session, async items => {
  const chats: TextChat[] = []
  for (const item of items) {
    offlineCache.set(item.msg_id, item)
    if (offlineCache.size > 1000) offlineCache.delete(offlineCache.keys().next().value!)
    if (item.content_type !== 1) continue
    const chat: TextChat = { id: item.id, msgId: item.msg_id, fromId: item.from_id, toId: item.to_id, senderType: item.sender_type, initiatorId: item.initiator_id, chatType: item.chat_type, content: item.content, createdAt: item.created_at, mentionedUserIds: item.mentioned_user_ids }
    chats.push(chat)
  }
  if (chats.length) await view.value?.applyOffline(chats)
})
async function pullOffline() {
  await offline.pull()
  offlineNotice.value = offline.state.error || (offline.state.morePending ? '仍有离线消息待处理，可继续补拉' : '')
}
realtime.connect()
function updateDraft(text: string) { if (key.value) drafts[key.value] = text }
function updateMentions(targets: MentionTarget[]) { if (key.value) mentions[key.value] = targets }
function sendMessage() {
  const selected = state.current, selectedKey = key.value
  if (!selected || !selectedKey || selected.key !== selectedKey || (selected.kind === 'group' && !selected.joined)) return
  const draft = drafts[selectedKey] ?? ''
  if (!draft.trim()) return
  const selectedMentions = selected.kind === 'group' ? mentions[selectedKey] ?? [] : []
  const mentionedUserIds = selectedMentions.map(target => target.id)
  const content = selectedMentions.length ? selectedMentions.map(target => `@${target.name}`).join(' ') + ' ' + draft : draft
  if (new TextEncoder().encode(content).length > 3000) { notices[selectedKey] = '消息不能超过 3000 字节'; return }
  if (connection.value !== 'connected') { notices[selectedKey] = '连接已断开，输入内容已保留'; return }
  if (!globalThis.crypto?.getRandomValues) { notices[selectedKey] = '当前浏览器无法安全生成消息标识'; return }
  const msgId = Array.from(crypto.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2, '0')).join('')
  const toId = selected.kind === 'group' ? selected.groupId : selected.key.slice('direct:'.length)
  if (!isId(toId)) { notices[selectedKey] = '会话标识无效，请重新打开会话'; return }
  const chatType = selected.kind === 'group' ? 2 : 1
  const item: Outgoing = { msgId, text: content, toId, chatType, mentionedUserIds, sentAt: Date.now(), status: 'sending' }
  const list = outgoing[selectedKey] ?? (outgoing[selectedKey] = [])
  list.push(item)
  if (!realtime.send({ msgId, toId, chatType, content, mentionedUserIds })) {
    if (item.status === 'uncertain') { drafts[selectedKey] = ''; mentions[selectedKey] = []; notices[selectedKey] = '发送结果待核对，请使用同一消息标识核对并重试'; return }
    list.splice(list.indexOf(item), 1)
    notices[selectedKey] = '上一条仍在提交，或连接暂时不可用，请稍后再试'
    return
  }
  drafts[selectedKey] = ''
  mentions[selectedKey] = []
  notices[selectedKey] = ''
}
async function checkAndRetry(msgId: string) {
  const item = locate(msgId), selectedKey = key.value, epoch = session.version()
  if (!item || !selectedKey || !(outgoing[selectedKey] ?? []).includes(item) || !view.value) return
  const persisted = await view.value.checkPersisted(msgId)
  if (persisted === true) { realtime.confirmPersisted(msgId); return }
  if (epoch !== session.version() || selectedKey !== key.value) return
  if (persisted === null) { notices[selectedKey] = '历史暂时无法核对，请稍后再试；当前不会重发'; return }
  if (item.status === 'accepted') { notices[selectedKey] = '服务已受理，历史暂未查到，请稍后再次核对'; return }
  if (item.status !== 'uncertain') return
  if (Date.now() - item.sentAt >= 5 * 60 * 1000) { notices[selectedKey] = '已超过服务端去重窗口，结果仍不确定；请先人工核对，不能安全自动重发'; return }
  if (!realtime.send({ msgId, toId: item.toId, chatType: item.chatType, content: item.text, mentionedUserIds: item.mentionedUserIds })) notices[selectedKey] = '连接暂不可用或上一条仍在提交，请稍后核对'
  else notices[selectedKey] = ''
}
void directory.loadTeams()
void directory.loadDirects()
onUnmounted(() => { realtime.dispose(); offline.dispose(); clearLocal(); directory.dispose() })
function revokeCurrent() {
  const selected = state.current
  if (selected) {
    delete drafts[selected.key]
    delete mentions[selected.key]
    delete outgoing[selected.key]
    delete notices[selected.key]
    for (const [id, message] of offlineCache) {
      const inScope = selected.kind === 'group'
        ? message.chat_type === 2 && message.to_id === selected.groupId
        : message.chat_type === 1 && (message.from_id === selected.key.slice('direct:'.length) || message.to_id === selected.key.slice('direct:'.length))
      if (inScope) offlineCache.delete(id)
    }
  }
  state.current = null
  state.detailError = '当前账号已无权访问这个会话'
}
</script>
<template>
  <main class="messages-layout">
    <ConversationList :state="state" :active-key="key" @teams="directory.loadTeams" @groups="directory.loadGroups" @directs="directory.loadDirects" />
    <div class="messages-workspace">
      <div class="sample-ribbon" role="note">{{ currentProfile?.nickname || currentProfile?.username }} · 真实会话与聊天</div>
      <UnreadOverview v-if="!key" />
      <section v-else-if="state.detailLoading" class="unavailable-state" role="status"><h1>正在复核会话…</h1><p>按当前登录身份检查访问权限。</p></section>
      <ConversationView v-else-if="state.current" :key="state.current.key" ref="view" :conversation="state.current" :own-id="currentProfile?.id || ''" :joining="state.joining" :error="state.detailError" :connection="connection" :draft="currentDraft" :mentions="currentMentions" :outgoing="currentOutgoing" :notice="currentNotice" :offline-notice="offlineNotice" @update:draft="updateDraft" @update:mentions="updateMentions" @send="sendMessage" @retry="checkAndRetry" @offline="pullOffline" @join="directory.joinCurrent" @revoked="revokeCurrent" />
      <section v-else class="unavailable-state">
        <div class="empty-symbol" aria-hidden="true">?</div><h1>会话暂时无法打开</h1>
        <p role="alert">{{ state.detailError || '请从左侧选择会话' }}</p>
        <button class="primary-link" type="button" @click="directory.select(key)">重新检查</button>
        <RouterLink class="primary-link" to="/messages">返回会话总览</RouterLink>
      </section>
    </div>
  </main>
</template>
