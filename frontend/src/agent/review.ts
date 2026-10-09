import { ApiError } from '../api/client.ts'
import type { createSession } from '../auth/session.ts'
import type { AgentScope, AgentTrigger, DraftCollection, DraftItem } from './model.ts'
import type { createAgentApi, TriggerScope } from './api.ts'

interface GroupScope { teamId: string; groupId: string }
type AgentApi = ReturnType<typeof createAgentApi>
type Identity = ReturnType<typeof createSession>
interface Clock {
  now(): number
  setTimeout(callback: () => void, delay: number): ReturnType<typeof setTimeout>
  clearTimeout(handle: ReturnType<typeof setTimeout>): void
}
export interface AgentReviewState {
  mode: 'closed' | 'ask' | 'trigger'
  group: GroupScope | null
  source: TriggerScope | null
  question: string
  answer: string
  askBusy: boolean
  askError: string
  trigger: AgentTrigger | null
  triggerBusy: boolean
  triggerError: string
  collection: DraftCollection | null
  collectionBusy: boolean
  collectionError: string
  selectedIndex: number | null
  itemBusy: boolean
  itemError: string
  counts: { waiting: number; created: number; skipped: number }
  textInput: { index: number; title: string; description: string } | null
  writeBusy: boolean
  writeError: string
  creatingReadyIndex: number | null
  replyRetryReadyIndex: number | null
}
export function initialAgentReviewState(): AgentReviewState {
  return { mode: 'closed', group: null, source: null, question: '', answer: '', askBusy: false, askError: '', trigger: null, triggerBusy: false, triggerError: '', collection: null, collectionBusy: false, collectionError: '', selectedIndex: null, itemBusy: false, itemError: '', counts: { waiting: 0, created: 0, skipped: 0 }, textInput: null, writeBusy: false, writeError: '', creatingReadyIndex: null, replyRetryReadyIndex: null }
}

const realClock: Clock = {
  now: () => Date.now(),
  setTimeout: (callback, delay) => setTimeout(callback, delay),
  clearTimeout: handle => clearTimeout(handle),
}
const trim = (value: string) => value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, '')
const validQuestion = (value: unknown): value is string => typeof value === 'string' && !/[\uD800-\uDFFF]/u.test(value) && [...trim(value)].length >= 1 && [...trim(value)].length <= 2000
const sameGroup = (a: GroupScope | null, b: GroupScope) => a?.teamId === b.teamId && a.groupId === b.groupId
const sameSource = (a: TriggerScope | null, b: TriggerScope) => sameGroup(a, b) && a?.messageId === b.messageId

export function createAgentReview(api: AgentApi, identity: Identity, state: AgentReviewState, clock: Clock = realClock) {
  let generation = 0
  let startedAt = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let triggerPending = false
  let collectionPending = false
  let itemPending = false
  let writePending = false
  let dataGeneration = 0
  let disposed = false
  const subscribed = identity.subscribe(() => close())

  function stopTimer() {
    if (timer !== undefined) clock.clearTimeout(timer)
    timer = undefined
  }
  function close() {
    generation++
    dataGeneration++
    stopTimer()
    triggerPending = false
    collectionPending = false
    itemPending = false
    writePending = false
    Object.assign(state, initialAgentReviewState())
  }
  function current(epoch: number, version: number) { return !disposed && epoch === generation && version === identity.version() }
  function currentRun(): AgentScope | null {
    if (state.mode !== 'trigger' || !state.group || state.trigger?.status !== 'completed' || state.trigger.run_id === '0') return null
    return { teamId: state.group.teamId, groupId: state.group.groupId, runId: state.trigger.run_id }
  }
  function sameRun(scope: AgentScope) {
    const run = currentRun()
    return run?.teamId === scope.teamId && run.groupId === scope.groupId && run.runId === scope.runId
  }
  function clearCollection() {
    dataGeneration++
    collectionPending = false
    itemPending = false
    writePending = false
    state.collection = null
    state.selectedIndex = null
    state.textInput = null
    state.collectionBusy = false
    state.itemBusy = false
    state.writeBusy = false
    state.writeError = ''
    state.creatingReadyIndex = null
    state.replyRetryReadyIndex = null
    state.counts = { waiting: 0, created: 0, skipped: 0 }
  }
  function recount(items: DraftItem[]) {
    state.counts = { waiting: items.filter(item => item.status === 'waiting_confirmation' || item.status === 'creating').length, created: items.filter(item => item.status === 'succeeded').length, skipped: items.filter(item => item.status === 'skipped').length }
  }
  function openAsk(group: GroupScope) {
    if (disposed) return
    if (state.mode === 'ask' && sameGroup(state.group, group)) return
    close()
    state.mode = 'ask'
    state.group = { ...group }
  }
  async function ask(question: string) {
    if (disposed || state.mode !== 'ask' || !state.group || state.askBusy) return
    state.question = typeof question === 'string' ? trim(question) : ''
    state.answer = ''
    state.askError = ''
    if (!validQuestion(question)) { state.askError = '请输入 1—2000 字的问题'; return }
    const epoch = generation, version = identity.version(), group = { ...state.group }
    state.askBusy = true
    try {
      const result = await api.ask(group.teamId, group.groupId, state.question)
      if (current(epoch, version)) state.answer = result.answer
    } catch (error) {
      if (!current(epoch, version)) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { close(); return }
      state.askError = error instanceof ApiError && error.status === 400 ? error.message : '本次回答结果未确认，可重新提问'
    } finally {
      if (current(epoch, version)) state.askBusy = false
    }
  }
  function schedule(epoch: number, version: number) {
    stopTimer()
    if (!current(epoch, version) || state.mode !== 'trigger' || !state.trigger || !['queued', 'running'].includes(state.trigger.status)) return
    const elapsed = Math.max(0, clock.now() - startedAt)
    if (elapsed >= 600000) return
    const delay = elapsed >= 120000 ? 15000 : 5000
    timer = clock.setTimeout(() => { timer = undefined; void refreshTrigger() }, delay)
  }
  async function refreshTrigger() {
    if (disposed || state.mode !== 'trigger' || !state.source || triggerPending) return
    stopTimer()
    const epoch = generation, version = identity.version(), source = { ...state.source }
    triggerPending = true
    state.triggerBusy = true
    state.triggerError = ''
    try {
      const result = await api.trigger(source)
      if (!current(epoch, version)) return
      if (state.trigger?.run_id !== result.run_id || result.status !== 'completed') clearCollection()
      state.trigger = result
    } catch (error) {
      if (!current(epoch, version)) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { close(); return }
      state.triggerError = '暂时无法核对处理状态，请稍后手动刷新'
    } finally {
      if (current(epoch, version)) {
        triggerPending = false
        state.triggerBusy = false
        schedule(epoch, version)
      }
    }
  }
  function openTrigger(source: TriggerScope) {
    if (disposed) return Promise.resolve()
    if (state.mode === 'trigger' && sameSource(state.source, source)) return refreshTrigger()
    close()
    state.mode = 'trigger'
    state.group = { teamId: source.teamId, groupId: source.groupId }
    state.source = { ...source }
    startedAt = clock.now()
    return refreshTrigger()
  }
  async function loadCollection() {
    const scope = currentRun()
    if (!scope || collectionPending || itemPending || writePending) return
    const epoch = generation, version = identity.version(), dataEpoch = dataGeneration
    collectionPending = true
    state.collectionBusy = true
    state.collectionError = ''
    try {
      const result = await api.collection(scope)
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
      if (result.run_id !== scope.runId || result.team_id !== scope.teamId || result.group_id !== scope.groupId || result.item_count !== result.items.length || result.item_count < 1 || result.item_count > 5 || result.items.some((item, index) => item.item_index !== index)) throw new ApiError(502, 'AI 草稿范围无效')
      const dirty = hasUnsavedText()
      if (dirty && state.textInput && state.textInput.index >= result.items.length) throw new ApiError(502, '未保存草稿项已不在集合中')
      const selectedIndex = state.selectedIndex !== null && state.selectedIndex < result.items.length ? state.selectedIndex : 0
      state.collection = result
      state.selectedIndex = selectedIndex
      state.creatingReadyIndex = null
      state.replyRetryReadyIndex = null
      if (!dirty) state.textInput = { index: selectedIndex, title: result.items[selectedIndex]!.draft.title, description: result.items[selectedIndex]!.draft.description }
      recount(result.items)
    } catch (error) {
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { close(); return }
      state.collectionError = '暂时无法读取草稿，请稍后重试'
    } finally {
      if (current(epoch, version) && dataEpoch === dataGeneration) { collectionPending = false; state.collectionBusy = false }
    }
  }
  function hasUnsavedText() {
    const input = state.textInput, item = input && state.collection?.items[input.index]
    return !!input && !!item && (input.title !== item.draft.title || input.description !== item.draft.description)
  }
  function updateText(title: string, description: string) {
    if (state.writeBusy || state.selectedIndex === null || !state.collection?.items[state.selectedIndex] || typeof title !== 'string' || typeof description !== 'string') return
    state.textInput = { index: state.selectedIndex, title, description }
  }
  function selectItem(index: number, discard = false): boolean {
    if (!state.collection || !Number.isInteger(index) || index < 0 || index >= state.collection.items.length || state.writeBusy || (hasUnsavedText() && !discard)) return false
    state.selectedIndex = index
    const item = state.collection.items[index]!
    state.textInput = { index, title: item.draft.title, description: item.draft.description }
    state.writeError = ''
    return true
  }
  async function reloadItem(index: number, internalRecheck = false) {
    const scope = currentRun()
    if (!scope || !state.collection || !Number.isInteger(index) || index < 0 || index >= state.collection.items.length || itemPending || collectionPending || (writePending && !internalRecheck)) return
    const epoch = generation, version = identity.version(), dataEpoch = dataGeneration
    itemPending = true
    state.itemBusy = true
    state.itemError = ''
    try {
      const result = await api.item(scope, index)
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope) || !state.collection) return
      if (result.item_index !== index) throw new ApiError(502, 'AI 草稿项无效')
      const replaceCleanInput = state.textInput?.index === index && !hasUnsavedText()
      state.collection = { ...state.collection, items: state.collection.items.map((item, i) => i === index ? result : item) }
      if (replaceCleanInput) state.textInput = { index, title: result.draft.title, description: result.draft.description }
      state.creatingReadyIndex = result.status === 'creating' ? index : null
      state.replyRetryReadyIndex = result.status === 'succeeded' && ['not_started', 'pending'].includes(result.reply_status) ? index : null
      recount(state.collection.items)
    } catch (error) {
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { close(); return }
      state.itemError = '暂时无法重读草稿项，请稍后重试'
    } finally {
      if (current(epoch, version) && dataEpoch === dataGeneration) { itemPending = false; state.itemBusy = false }
    }
  }
  async function writeItem(index: number, operation: (scope: AgentScope, item: DraftItem) => Promise<DraftItem>, resetText = false) {
    const scope = currentRun(), item = state.collection?.items[index]
    if (!scope || !item || item.status !== 'waiting_confirmation' || !Number.isInteger(index) || index < 0 || index > 4 || writePending || itemPending || collectionPending) return
    const epoch = generation, version = identity.version(), dataEpoch = dataGeneration
    const submittedInput = state.textInput, replaceCleanInput = !hasUnsavedText()
    writePending = true
    state.writeBusy = true
    state.writeError = ''
    try {
      const result = await operation(scope, item)
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope) || !state.collection) return
      if (result.item_index !== index) throw new ApiError(502, 'AI 草稿项无效')
      state.collection = { ...state.collection, items: state.collection.items.map((row, i) => i === index ? result : row) }
      if (state.textInput === submittedInput && submittedInput?.index === index && (resetText || replaceCleanInput)) state.textInput = { index, title: result.draft.title, description: result.draft.description }
      state.creatingReadyIndex = null
      state.replyRetryReadyIndex = null
      recount(state.collection.items)
    } catch (error) {
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { close(); return }
      if (!(error instanceof ApiError && error.status === 400)) {
        await reloadItem(index, true)
        if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
        state.writeError = state.itemError ? '保存结果未确认，重新核对失败，请稍后重试' : '已重新核对草稿项，请对照保存状态和本地输入'
      } else state.writeError = error.message
    } finally {
      if (current(epoch, version) && dataEpoch === dataGeneration) { writePending = false; state.writeBusy = false }
    }
  }
  function editText(index: number) {
    const input = state.textInput
    if (!input || input.index !== index || !hasUnsavedText()) return Promise.resolve()
    const title = trim(input.title), description = trim(input.description)
    if (!title || [...title].length > 200 || [...description].length > 2000 || /[\uD800-\uDFFF]/u.test(title + description)) { state.writeError = '标题需为 1—200 字，说明最多 2000 字'; return Promise.resolve() }
    return writeItem(index, (scope, item) => api.editText(scope, index, { title, description, expected_revision: item.draft.revision }), true)
  }
  function selectAssignee(index: number, assigneeId: string) {
    return writeItem(index, (scope, item) => api.selectAssignee(scope, index, { assignee_id: assigneeId, expected_revision: item.draft.revision }))
  }
  function editDeadline(index: number, dueAtUnixMs: number) {
    return writeItem(index, (scope, item) => api.editDeadline(scope, index, { due_at_unix_ms: dueAtUnixMs, expected_revision: item.draft.revision }))
  }
  function decisionItem(index: number) {
    return Number.isInteger(index) && index >= 0 && index <= 4 && currentRun() ? state.collection?.items[index] : undefined
  }
  function canConfirm(index: number) {
    const item = decisionItem(index)
    if (!item || writePending || itemPending || collectionPending || hasUnsavedText() || ['not_found', 'ambiguous', 'truncated'].includes(item.draft.assignee_resolution) || item.draft.deadline.resolution === 'needs_input') return false
    return item.status === 'waiting_confirmation' || (item.status === 'creating' && state.creatingReadyIndex === index)
  }
  function canSkip(index: number) {
    return decisionItem(index)?.status === 'waiting_confirmation' && !writePending && !itemPending && !collectionPending && !hasUnsavedText()
  }
  function canRetryReply(index: number) {
    const item = decisionItem(index)
    return !!item && item.status === 'succeeded' && state.replyRetryReadyIndex === index && ['not_started', 'pending'].includes(item.reply_status) && !writePending && !itemPending && !collectionPending
  }
  async function decide(index: number, kind: 'confirm' | 'skip' | 'reply') {
    if (!(kind === 'confirm' ? canConfirm(index) : kind === 'skip' ? canSkip(index) : canRetryReply(index))) return
    const scope = currentRun()!, item = state.collection!.items[index]!
    const epoch = generation, version = identity.version(), dataEpoch = dataGeneration
    writePending = true
    state.writeBusy = true
    state.writeError = ''
    state.creatingReadyIndex = null
    state.replyRetryReadyIndex = null
    try {
      const result = kind === 'confirm' ? await api.confirm(scope, index, {
        expected_title: item.draft.title, expected_description: item.draft.description,
        expected_revision: item.draft.revision, expected_assignee_id: item.draft.assignee_id,
        expected_due_at_unix_ms: item.draft.due_at_unix_ms, expected_deadline_resolution: item.draft.deadline.resolution,
      }) : kind === 'skip' ? await api.skip(scope, index, { expected_revision: item.draft.revision }) : await api.retryReply(scope, index)
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope) || !state.collection) return
      if (result.item_index !== index) throw new ApiError(502, 'AI 草稿项无效')
      state.collection = { ...state.collection, items: state.collection.items.map((row, i) => i === index ? result : row) }
      recount(state.collection.items)
    } catch (error) {
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { close(); return }
      if (!(error instanceof ApiError && error.status === 400)) {
        await reloadItem(index, true)
        if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
        state.writeError = state.itemError ? '操作结果未确认，重读本项失败，请稍后重试' : '已重读本项，请核对任务与回帖状态后再操作'
      } else state.writeError = error.message
    } finally {
      if (current(epoch, version) && dataEpoch === dataGeneration) { writePending = false; state.writeBusy = false }
    }
  }
  const confirm = (index: number) => decide(index, 'confirm')
  const skip = (index: number) => decide(index, 'skip')
  const retryReply = (index: number) => decide(index, 'reply')
  function dispose() {
    if (disposed) return
    disposed = true
    subscribed()
    close()
  }
  return { openAsk, ask, openTrigger, refreshTrigger, loadCollection, selectItem, reloadItem, updateText, hasUnsavedText, editText, selectAssignee, editDeadline, canConfirm, canSkip, canRetryReply, confirm, skip, retryReply, close, dispose }
}
