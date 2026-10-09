<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { api } from '../api/client.ts'
import { session } from '../auth/session.ts'
import { createMemberDirectory, initialMemberDirectoryState, shanghaiDateTimeToUnixMs } from '../tasks/create.ts'
import type { AgentReviewState, createAgentReview } from './review.ts'

const props = defineProps<{ state: AgentReviewState; review: ReturnType<typeof createAgentReview> }>()
const emit = defineEmits<{ close: []; fillInstruction: [] }>()
const selected = computed(() => props.state.selectedIndex === null ? null : props.state.collection?.items[props.state.selectedIndex] ?? null)
const memberState = reactive(initialMemberDirectoryState())
const members = createMemberDirectory(api.request, memberState)
const assigneeChoice = ref('')
const dueLocal = ref('')
const assigneeBaseline = ref('')
const dueBaseline = ref('')
const formError = ref('')
const askInput = ref('')
const closeButton = ref<HTMLButtonElement | null>(null)

const shanghaiValue = (milliseconds: number) => milliseconds > 0 ? new Date(milliseconds + 8 * 60 * 60 * 1000).toISOString().slice(0, 16) : ''
const shanghaiText = (milliseconds: number) => milliseconds > 0 ? new Intl.DateTimeFormat('zh-CN', { timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(new Date(milliseconds)) : '未设置'
const sourceLink = (messageId: string) => ({ name: 'group', params: { teamId: props.state.group?.teamId, groupId: props.state.group?.groupId }, query: { focus_message_id: messageId } })
const editable = computed(() => selected.value?.status === 'waiting_confirmation' && !props.state.writeBusy && !props.state.itemBusy)
const selectedAssigneeValid = computed(() => assigneeChoice.value === '0' || memberState.items.some(member => member.user_id === assigneeChoice.value) || (assigneeChoice.value !== '' && assigneeChoice.value === selected.value?.draft.assignee_id))
const savedAssigneeOutsidePage = computed(() => !!selected.value && selected.value.draft.assignee_id !== '0' && !memberState.items.some(member => member.user_id === selected.value?.draft.assignee_id))
const hasUnsavedChoice = computed(() => assigneeChoice.value !== assigneeBaseline.value || dueLocal.value !== dueBaseline.value)
const hasUnsavedInput = computed(() => props.review.hasUnsavedText() || hasUnsavedChoice.value)
defineExpose({ hasUnsavedInput: () => hasUnsavedInput.value })

watch(() => props.state.mode, mode => { if (mode === 'ask') askInput.value = props.state.question }, { immediate: true })
watch(() => [props.state.mode, props.state.source?.messageId], async () => { await nextTick(); closeButton.value?.focus() })
onMounted(async () => { await nextTick(); closeButton.value?.focus() })

watch(() => props.state.group?.teamId, teamId => {
  if (teamId) void members.select(teamId)
  else members.dispose()
}, { immediate: true })
watch(() => [props.state.selectedIndex, selected.value?.draft.revision], (_next, previous) => {
  const draft = selected.value?.draft
  const newAssignee = draft && !['not_found', 'ambiguous', 'truncated'].includes(draft.assignee_resolution) ? draft.assignee_id : ''
  const newDue = draft ? shanghaiValue(draft.due_at_unix_ms) : ''
  if (!previous || previous[0] !== props.state.selectedIndex || assigneeChoice.value === assigneeBaseline.value) assigneeChoice.value = newAssignee
  if (!previous || previous[0] !== props.state.selectedIndex || dueLocal.value === dueBaseline.value) dueLocal.value = newDue
  assigneeBaseline.value = newAssignee
  dueBaseline.value = newDue
  formError.value = ''
}, { immediate: true })
const unsubscribe = session.subscribe(() => members.dispose())
onUnmounted(() => { unsubscribe(); members.dispose() })

function changeItem(index: number) {
  if (props.state.selectedIndex === index) return
  if (hasUnsavedInput.value && !window.confirm('当前草稿有未保存修改。放弃修改并切换草稿项？')) return
  props.review.selectItem(index, true)
}
function closePanel() {
  if (hasUnsavedInput.value && !window.confirm('当前草稿有未保存修改。放弃修改并关闭审查？')) return
  emit('close')
}
function saveText() {
  if (selected.value && props.state.textInput?.index === selected.value.item_index) void props.review.editText(selected.value.item_index)
}
function saveAssignee() {
  if (selected.value && selectedAssigneeValid.value) void props.review.selectAssignee(selected.value.item_index, assigneeChoice.value)
}
function saveDeadline() {
  if (!selected.value || !dueLocal.value) return
  try { formError.value = ''; void props.review.editDeadline(selected.value.item_index, shanghaiDateTimeToUnixMs(dueLocal.value)) }
  catch (error) { formError.value = error instanceof Error ? error.message : '截止时间无效' }
}
function clearDeadline() {
  if (selected.value) { formError.value = ''; void props.review.editDeadline(selected.value.item_index, 0) }
}
function confirmItem() {
  if (selected.value && !hasUnsavedInput.value) void props.review.confirm(selected.value.item_index)
}
function skipItem() {
  if (selected.value && !hasUnsavedInput.value) void props.review.skip(selected.value.item_index)
}
function retryReply() {
  if (selected.value) void props.review.retryReply(selected.value.item_index)
}
function submitAsk() { void props.review.ask(askInput.value) }
</script>

<template>
  <aside class="task-detail-panel agent-panel" :aria-label="state.mode === 'ask' ? 'AI 助手' : 'AI 草稿审查'">
    <header class="task-detail-header"><div><p>当前团队群 · AI 助手</p><h2>{{ state.mode === 'ask' ? '向 AI 提问' : '逐项审查' }}</h2></div><button ref="closeButton" type="button" :aria-label="state.mode === 'ask' ? '关闭 AI 助手' : '关闭 AI 草稿审查'" @click="closePanel">×</button></header>
    <section v-if="state.mode === 'ask'" class="agent-ask">
      <p>回答仅在这次打开的面板中显示，不会发送到群聊。</p>
      <form @submit.prevent="submitAsk"><label>问题<textarea v-model="askInput" rows="5" :disabled="state.askBusy" placeholder="请根据当前团队的讨论回答我的问题" /><span>{{ [...askInput].length }}/2000 字</span></label><button class="primary-link" type="submit" :disabled="state.askBusy || !askInput.trim() || [...askInput.trim()].length > 2000">{{ state.askBusy ? '正在提问…' : '提问' }}</button></form>
      <p v-if="state.askBusy" role="status">正在等待 AI 回答…</p>
      <p v-if="state.askError" role="alert">{{ state.askError }}</p>
      <section v-if="state.answer" aria-label="AI 回答"><h3>回答</h3><p class="agent-answer">{{ state.answer }}</p></section>
      <button type="button" class="task-secondary" @click="emit('fillInstruction')">整理讨论事项：填入群聊指令</button>
      <p class="agent-hint">此操作只填入输入框，发送前可继续编辑。</p>
    </section>
    <p v-if="state.trigger?.status === 'queued' || state.trigger?.status === 'running'" role="status">AI 正在整理当前指令…</p>
    <p v-if="state.trigger?.status === 'exhausted'" role="alert">本次整理未成功。请查看原消息后重新发送明确指令。</p>
    <p v-if="state.triggerError" role="alert">{{ state.triggerError }}</p>
    <button v-if="state.mode === 'trigger'" type="button" class="task-secondary" :disabled="state.triggerBusy" @click="review.refreshTrigger()">刷新处理状态</button>
    <section v-if="state.trigger?.status === 'completed'" class="agent-review">
      <p v-if="state.collection" role="status">待审查 {{ state.counts.waiting }} · 已创建 {{ state.counts.created }} · 已跳过 {{ state.counts.skipped }}</p>
      <button v-if="!state.collection" type="button" :disabled="state.collectionBusy" @click="review.loadCollection()">{{ state.collectionBusy ? '正在读取草稿…' : '读取草稿' }}</button>
      <p v-if="state.collectionError" role="alert">{{ state.collectionError }}</p>
      <div v-if="state.collection" class="agent-item-list" aria-label="草稿项">
        <button v-for="item in state.collection.items" :key="item.item_index" type="button" :aria-current="state.selectedIndex === item.item_index ? 'true' : undefined" @click="changeItem(item.item_index)">第 {{ item.item_index + 1 }} 项 · {{ item.draft.title }} · {{ item.status === 'succeeded' ? '已创建' : item.status === 'skipped' ? '已跳过' : item.status === 'creating' ? '创建中' : '待审查' }}</button>
      </div>
      <section v-if="selected" class="agent-item-detail" :aria-label="`第 ${selected.item_index + 1} 项详情`">
        <p v-if="selected.draft.source_message_id !== '0'"><RouterLink :to="sourceLink(selected.draft.source_message_id)">查看这项草稿的讨论来源</RouterLink></p>
        <p v-else>这项草稿没有单独的讨论来源消息</p>
        <p>当前状态：{{ selected.status === 'waiting_confirmation' ? '待审查' : selected.status === 'creating' ? '创建中' : selected.status === 'succeeded' ? '已创建' : '已跳过' }}</p>
        <div v-if="selected.status === 'waiting_confirmation'" class="task-form">
          <label>标题<input :value="state.textInput?.title ?? selected.draft.title" :disabled="!editable" @input="review.updateText(($event.target as HTMLInputElement).value, state.textInput?.description ?? selected.draft.description)" /><span>{{ [...(state.textInput?.title ?? selected.draft.title)].length }}/200 字</span></label>
          <label>说明<textarea :value="state.textInput?.description ?? selected.draft.description" rows="4" :disabled="!editable" @input="review.updateText(state.textInput?.title ?? selected.draft.title, ($event.target as HTMLTextAreaElement).value)" /><span>{{ [...(state.textInput?.description ?? selected.draft.description)].length }}/2000 字</span></label>
          <p v-if="review.hasUnsavedText()" role="status">文字有未保存修改；请保存或放弃后继续审查。</p>
          <p v-if="hasUnsavedChoice" role="status">负责人或期限选择尚未保存；请分别保存或放弃。</p>
          <button type="button" :disabled="!editable || !review.hasUnsavedText()" @click="saveText">保存标题和说明</button>
          <section aria-label="负责人依据">
            <p>AI 提取的负责人称呼：{{ selected.draft.assignee_name || '未提取' }}；解析状态：{{ selected.draft.assignee_resolution }}</p>
            <p>当前保存的负责人：{{ selected.draft.assignee_id === '0' ? '未分配' : selected.draft.assignee_name || `成员 ${selected.draft.assignee_id}` }}</p>
            <p v-if="['not_found', 'ambiguous', 'truncated'].includes(selected.draft.assignee_resolution)" role="alert">负责人尚不明确，请明确选一位当前团队成员，或选择未分配并保存。</p>
            <label>明确选择负责人<select v-model="assigneeChoice" :disabled="!editable || memberState.loading"><option value="" disabled>请选择</option><option value="0">未分配</option><option v-if="savedAssigneeOutsidePage" :value="selected.draft.assignee_id">当前保存：{{ selected.draft.assignee_name || selected.draft.assignee_id }}</option><option v-for="member in memberState.items" :key="member.user_id" :value="member.user_id">{{ member.nickname.trim() || member.username }}</option></select></label>
            <button type="button" :disabled="!editable || !selectedAssigneeValid" @click="saveAssignee">保存负责人选择</button>
            <button v-if="memberState.cursor !== '0'" type="button" :disabled="memberState.loading" @click="members.load()">加载更多成员</button>
            <p v-if="memberState.error" role="alert">{{ memberState.error }} <button type="button" @click="members.load()">重试</button></p>
          </section>
          <section aria-label="期限依据">
            <p>原始时间：{{ selected.draft.deadline.text || '未提取' }}；来源：{{ selected.draft.deadline.source }}；时区：{{ selected.draft.deadline.timezone }}</p>
            <p v-if="selected.draft.deadline.source_message_id !== '0'"><RouterLink :to="sourceLink(selected.draft.deadline.source_message_id)">查看时间依据消息</RouterLink></p>
            <p>原解析候选：{{ shanghaiText(selected.draft.deadline.parsed_unix_ms) }}；解析状态：{{ selected.draft.deadline.resolution }}{{ selected.draft.deadline.reason ? `（${selected.draft.deadline.reason}）` : '' }}</p>
            <p>当前保存的截止时间：{{ shanghaiText(selected.draft.due_at_unix_ms) }}</p>
            <p v-if="selected.draft.deadline.resolution === 'needs_input'" role="alert">期限尚不明确，请明确设置上海时间或选择不设期限并保存。</p>
            <label>截止时间（Asia/Shanghai）<input v-model="dueLocal" type="datetime-local" :disabled="!editable" /></label>
            <button type="button" :disabled="!editable || !dueLocal" @click="saveDeadline">保存截止时间</button>
            <button type="button" :disabled="!editable" @click="clearDeadline">明确选择不设期限</button>
          </section>
          <div class="task-form-actions">
            <button type="button" class="primary-link" :disabled="hasUnsavedInput || !review.canConfirm(selected.item_index)" @click="confirmItem">确认创建第 {{ selected.item_index + 1 }} 项</button>
            <button type="button" class="task-secondary" :disabled="hasUnsavedInput || !review.canSkip(selected.item_index)" @click="skipItem">跳过第 {{ selected.item_index + 1 }} 项</button>
          </div>
        </div>
        <div v-if="selected.status === 'creating'">
          <p role="status">任务创建结果正在核对；当前项暂不能编辑或跳过。</p>
          <button type="button" :disabled="state.itemBusy || state.writeBusy" @click="review.reloadItem(selected.item_index)">重读第 {{ selected.item_index + 1 }} 项创建状态</button>
          <button v-if="state.creatingReadyIndex === selected.item_index" type="button" :disabled="!review.canConfirm(selected.item_index)" @click="confirmItem">继续确认同一项</button>
        </div>
        <div v-if="selected.status === 'succeeded'">
          <p>任务已创建：<RouterLink :to="{ name: 'task-detail', params: { teamId: state.group?.teamId, taskId: selected.task_id } }">查看任务 {{ selected.task_id }}</RouterLink></p>
          <p v-if="selected.reply_status === 'accepted'" role="status">群回帖已由 IM 受理；送达和已读状态尚未确认。</p>
          <p v-else-if="selected.reply_status === 'unknown'" role="status">群回帖结果未确认，请核对；页面不会自动重发。</p>
          <p v-else-if="selected.reply_status === 'pending'" role="status">群回帖处理中，尚未确认 IM 受理。</p>
          <p v-else-if="selected.reply_status === 'not_started'" role="status">群回帖尚未开始，任务已独立创建。</p>
          <p v-else>当前任务不需要群回帖。</p>
          <button v-if="['not_started', 'pending', 'unknown'].includes(selected.reply_status)" type="button" :disabled="state.itemBusy || state.writeBusy" @click="review.reloadItem(selected.item_index)">核对第 {{ selected.item_index + 1 }} 项回帖状态</button>
          <button v-if="review.canRetryReply(selected.item_index)" type="button" @click="retryReply">重试第 {{ selected.item_index + 1 }} 项群回帖</button>
        </div>
        <p v-if="selected.status === 'skipped'" role="status">本项已跳过，不会创建任务或发送群回帖。</p>
        <p v-if="state.writeBusy" role="status">正在保存当前草稿项…</p>
        <p v-if="state.writeError" role="alert">{{ state.writeError }}</p>
        <p v-if="state.itemError" role="alert">{{ state.itemError }} <button type="button" @click="review.reloadItem(selected.item_index)">重读本项</button></p>
        <p v-if="formError" role="alert">{{ formError }}</p>
      </section>
    </section>
  </aside>
</template>
