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
}
export function initialAgentReviewState(): AgentReviewState {
  return { mode: 'closed', group: null, source: null, question: '', answer: '', askBusy: false, askError: '', trigger: null, triggerBusy: false, triggerError: '', collection: null, collectionBusy: false, collectionError: '', selectedIndex: null, itemBusy: false, itemError: '', counts: { waiting: 0, created: 0, skipped: 0 } }
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
    state.collection = null
    state.selectedIndex = null
    state.collectionBusy = false
    state.itemBusy = false
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
    if (!scope || collectionPending) return
    const epoch = generation, version = identity.version(), dataEpoch = dataGeneration
    collectionPending = true
    state.collectionBusy = true
    state.collectionError = ''
    try {
      const result = await api.collection(scope)
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
      if (result.run_id !== scope.runId || result.team_id !== scope.teamId || result.group_id !== scope.groupId || result.item_count !== result.items.length || result.item_count < 1 || result.item_count > 5 || result.items.some((item, index) => item.item_index !== index)) throw new ApiError(502, 'AI 草稿范围无效')
      state.collection = result
      state.selectedIndex = 0
      recount(result.items)
    } catch (error) {
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { close(); return }
      state.collectionError = '暂时无法读取草稿，请稍后重试'
    } finally {
      if (current(epoch, version) && dataEpoch === dataGeneration) { collectionPending = false; state.collectionBusy = false }
    }
  }
  function selectItem(index: number) {
    if (!state.collection || !Number.isInteger(index) || index < 0 || index >= state.collection.items.length) return
    state.selectedIndex = index
  }
  async function reloadItem(index: number) {
    const scope = currentRun()
    if (!scope || !state.collection || !Number.isInteger(index) || index < 0 || index >= state.collection.items.length || itemPending) return
    const epoch = generation, version = identity.version(), dataEpoch = dataGeneration
    itemPending = true
    state.itemBusy = true
    state.itemError = ''
    try {
      const result = await api.item(scope, index)
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope) || !state.collection) return
      if (result.item_index !== index) throw new ApiError(502, 'AI 草稿项无效')
      state.collection = { ...state.collection, items: state.collection.items.map((item, i) => i === index ? result : item) }
      recount(state.collection.items)
    } catch (error) {
      if (!current(epoch, version) || dataEpoch !== dataGeneration || !sameRun(scope)) return
      if (error instanceof ApiError && (error.status === 403 || error.status === 404)) { close(); return }
      state.itemError = '暂时无法重读草稿项，请稍后重试'
    } finally {
      if (current(epoch, version) && dataEpoch === dataGeneration) { itemPending = false; state.itemBusy = false }
    }
  }
  function dispose() {
    if (disposed) return
    disposed = true
    subscribed()
    close()
  }
  return { openAsk, ask, openTrigger, refreshTrigger, loadCollection, selectItem, reloadItem, close, dispose }
}
