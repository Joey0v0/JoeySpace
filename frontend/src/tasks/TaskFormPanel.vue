<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { api, isId } from '../api/client.ts'
import { session } from '../auth/session.ts'
import { createSourceContext, initialSourceContextState } from '../messages/sourceContext.ts'
import { createTaskCreator, initialCreateState, type CreateForm } from './create.ts'
import type { TaskDetail } from './model.ts'
import type { TeamOption } from './workspace.ts'
const props = defineProps<{ teams: TeamOption[]; defaultTeamId: string; sourceGroupId: string; sourceMessageId: string; invalidSource: boolean }>()
const emit = defineEmits<{ close: []; created: [detail: TaskDetail] }>()
const form = reactive<CreateForm>({ teamId: props.defaultTeamId, title: '', description: '', assigneeId: '0', sourceGroupId: props.sourceGroupId, sourceMessageId: props.sourceMessageId, dueLocal: '' })
const createState = reactive(initialCreateState())
const creator = createTaskCreator(api.request, session, createState)
const sourceState = reactive(initialSourceContextState())
const source = createSourceContext(api.request, session, sourceState)
interface Member { user_id: string; username: string; nickname: string }
const members = ref<Member[]>([]), memberCursor = ref('0'), membersLoading = ref(false), membersError = ref('')
let memberScope = 0
const hasSource = computed(() => isId(form.sourceGroupId) && isId(form.sourceMessageId))
const frozen = computed(() => createState.frozen || createState.phase === 'submitting')
const sourceReady = computed(() => !hasSource.value || (!!sourceState.context && !sourceState.loading && !sourceState.error))
const target = computed(() => sourceState.context?.messages.find(item => item.id === sourceState.context?.target_message_id))
async function loadMembers(reset = false) {
  if (!isId(form.teamId) || membersLoading.value) return
  if (reset) { memberScope++; members.value = []; memberCursor.value = '0'; form.assigneeId = '0' }
  const ticket = memberScope, after = memberCursor.value; membersLoading.value = true; membersError.value = ''
  try {
    const data = await api.request<{ members: Member[]; next_after_user_id: string }>(`/teams/${form.teamId}/members?after_user_id=${after}&limit=100`)
    if (ticket !== memberScope) return
    if (!data || !Array.isArray(data.members) || !data.members.every(item => isId(item.user_id) && typeof item.username === 'string' && typeof item.nickname === 'string') || (data.next_after_user_id !== '0' && !isId(data.next_after_user_id))) throw new Error('invalid')
    members.value = [...new Map([...members.value, ...data.members].map(item => [item.user_id, item])).values()]
    memberCursor.value = data.next_after_user_id
  } catch { if (ticket === memberScope) membersError.value = '成员目录暂时不可用，请重试' }
  finally { if (ticket === memberScope) membersLoading.value = false }
}
watch(() => form.teamId, () => { void loadMembers(true) })
watch(() => createState.detail, detail => { if (detail) emit('created', detail) })
const clearForm = session.subscribe(() => { memberScope++; Object.assign(form, { teamId: '', title: '', description: '', assigneeId: '0', sourceGroupId: '0', sourceMessageId: '0', dueLocal: '' }); members.value = []; memberCursor.value = '0' })
onMounted(() => {
  if (isId(form.teamId)) void loadMembers(true)
  if (hasSource.value) void source.load(form.teamId, form.sourceGroupId, form.sourceMessageId)
})
onUnmounted(() => { memberScope++; clearForm(); creator.dispose(); source.dispose() })
function submit() { if (!props.invalidSource && sourceReady.value) void creator.submit(form) }
</script>
<template>
  <aside class="task-detail-panel task-form-panel" aria-label="新建任务">
    <header class="task-detail-header"><div><p>新建任务</p><h2>记录团队事项</h2></div><button type="button" aria-label="关闭新建任务" @click="emit('close')">×</button></header>
    <form class="task-form" @submit.prevent="submit">
      <label>团队<select v-model="form.teamId" required :disabled="frozen || hasSource"><option value="" disabled>请选择团队</option><option v-for="team in teams" :key="team.team_id" :value="team.team_id">{{ team.name }}</option><option v-if="hasSource && !teams.some(team => team.team_id === form.teamId)" :value="form.teamId">来源团队 {{ form.teamId }}</option></select></label>
      <label>标题<input v-model="form.title" required maxlength="200" :disabled="frozen" /><span>{{ [...form.title].length }}/200</span></label>
      <label>说明<textarea v-model="form.description" maxlength="2000" rows="5" :disabled="frozen" /><span>{{ [...form.description].length }}/2000</span></label>
      <label>负责人<select v-model="form.assigneeId" :disabled="frozen || membersLoading"><option value="0">未分配</option><option v-for="member in members" :key="member.user_id" :value="member.user_id">{{ member.nickname.trim() || member.username }}</option></select></label>
      <button v-if="memberCursor !== '0'" class="task-secondary" type="button" :disabled="frozen || membersLoading" @click="loadMembers()">{{ membersLoading ? '正在加载…' : '加载更多成员' }}</button>
      <p v-if="membersError" role="alert">{{ membersError }} <button type="button" @click="loadMembers()">重试</button></p>
      <label>截止时间（上海时间）<input v-model="form.dueLocal" type="datetime-local" :disabled="frozen" /></label>
      <section v-if="hasSource || invalidSource" class="task-source-preview">
        <h3>讨论来源</h3>
        <p v-if="invalidSource" role="alert">来源地址无效，请返回群聊重新选择消息。</p>
        <p v-else-if="sourceState.loading" role="status">正在重新核对来源消息…</p>
        <p v-else-if="sourceState.error" role="alert">{{ sourceState.error }} <button type="button" @click="source.load(form.teamId, form.sourceGroupId, form.sourceMessageId)">重新检查</button></p>
        <p v-else-if="target" class="source-snippet">{{ target.content_type === 1 ? target.content : '[其他类型消息]' }}</p>
      </section>
      <p v-if="createState.status" role="status">{{ createState.status }}</p>
      <p v-if="createState.error" role="alert">{{ createState.error }}</p>
      <div class="task-form-actions">
        <button v-if="createState.phase === 'uncertain' || createState.phase === 'detail-uncertain'" class="primary-link" type="button" @click="creator.retry">核对并重试</button>
        <button v-else class="primary-link" type="submit" :disabled="frozen || invalidSource || !sourceReady || !isId(form.teamId)">{{ createState.phase === 'submitting' ? '正在提交…' : '创建任务' }}</button>
        <button class="task-secondary" type="button" :disabled="frozen" @click="emit('close')">取消</button>
      </div>
    </form>
  </aside>
</template>
