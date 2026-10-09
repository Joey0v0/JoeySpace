<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { api, isId } from '../api/client.ts'
import { session } from '../auth/session.ts'
import { createSourceContext, initialSourceContextState } from '../messages/sourceContext.ts'
import { createMemberDirectory, createTaskCreator, initialCreateState, initialMemberDirectoryState, type CreateForm } from './create.ts'
import type { TaskDetail } from './model.ts'
import type { TeamOption } from './workspace.ts'
const props = defineProps<{ teams: TeamOption[]; defaultTeamId: string; sourceGroupId: string; sourceMessageId: string; invalidSource: boolean }>()
const emit = defineEmits<{ close: []; created: [detail: TaskDetail] }>()
const form = reactive<CreateForm>({ teamId: props.defaultTeamId, title: '', description: '', assigneeId: '0', sourceGroupId: props.sourceGroupId, sourceMessageId: props.sourceMessageId, dueLocal: '' })
const createState = reactive(initialCreateState())
const creator = createTaskCreator(api.request, session, createState)
const sourceState = reactive(initialSourceContextState())
const source = createSourceContext(api.request, session, sourceState)
const memberState = reactive(initialMemberDirectoryState())
const memberDirectory = createMemberDirectory(api.request, memberState)
const closeButton = ref<HTMLButtonElement | null>(null)
const hasSource = computed(() => isId(form.sourceGroupId) && isId(form.sourceMessageId))
const frozen = computed(() => createState.frozen || createState.phase === 'submitting')
const sourceReady = computed(() => !hasSource.value || (!!sourceState.context && !sourceState.loading && !sourceState.error))
const target = computed(() => sourceState.context?.messages.find(item => item.id === sourceState.context?.target_message_id))
function selectTeam(teamId = form.teamId) { form.teamId = teamId; form.assigneeId = '0'; void memberDirectory.select(teamId) }
watch(() => createState.detail, detail => { if (detail) emit('created', detail) })
const clearForm = session.subscribe(() => { memberDirectory.dispose(); Object.assign(form, { teamId: '', title: '', description: '', assigneeId: '0', sourceGroupId: '0', sourceMessageId: '0', dueLocal: '' }) })
onMounted(async () => {
  await nextTick(); closeButton.value?.focus()
  if (isId(form.teamId)) selectTeam()
  if (hasSource.value) void source.load(form.teamId, form.sourceGroupId, form.sourceMessageId)
})
onUnmounted(() => { memberDirectory.dispose(); clearForm(); creator.dispose(); source.dispose() })
function submit() { if (!props.invalidSource && sourceReady.value) void creator.submit(form) }
</script>
<template>
  <aside class="task-detail-panel task-form-panel" aria-label="新建任务">
    <header class="task-detail-header"><div><p>新建任务</p><h2>记录团队事项</h2></div><button ref="closeButton" type="button" aria-label="关闭新建任务" @click="emit('close')">×</button></header>
    <form class="task-form" @submit.prevent="submit">
      <label>团队<select :value="form.teamId" required :disabled="frozen || hasSource" @change="selectTeam(($event.target as HTMLSelectElement).value)"><option value="" disabled>请选择团队</option><option v-for="team in teams" :key="team.team_id" :value="team.team_id">{{ team.name }}</option><option v-if="hasSource && !teams.some(team => team.team_id === form.teamId)" :value="form.teamId">来源团队 {{ form.teamId }}</option></select></label>
      <label>标题<input v-model="form.title" required maxlength="200" :disabled="frozen" /><span>{{ [...form.title].length }}/200</span></label>
      <label>说明<textarea v-model="form.description" maxlength="2000" rows="5" :disabled="frozen" /><span>{{ [...form.description].length }}/2000</span></label>
      <label>负责人<select v-model="form.assigneeId" :disabled="frozen || memberState.loading"><option value="0">未分配</option><option v-for="member in memberState.items" :key="member.user_id" :value="member.user_id">{{ member.nickname.trim() || member.username }}</option></select></label>
      <button v-if="memberState.cursor !== '0'" class="task-secondary" type="button" :disabled="frozen || memberState.loading" @click="memberDirectory.load()">{{ memberState.loading ? '正在加载…' : '加载更多成员' }}</button>
      <p v-if="memberState.error" role="alert">{{ memberState.error }} <button type="button" @click="memberDirectory.load()">重试</button></p>
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
