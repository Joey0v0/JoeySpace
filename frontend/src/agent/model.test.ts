import test from 'node:test'
import assert from 'node:assert/strict'
import { isOwnAgentCommand, decodeAgentTrigger, decodeDraftCollection, decodeDraftItem } from './model.ts'
import type { ChatMessage } from '../messages/history.ts'

const scope = { teamId: '9007199254740993', groupId: '9223372036854775807', runId: '9007199254740995' }
const triggerScope = { teamId: scope.teamId, groupId: scope.groupId, messageId: '9007199254740997' }
const message = (patch: Partial<ChatMessage> = {}): ChatMessage => ({ id: triggerScope.messageId, msg_id: 'saved', from_id: '9007199254740999', sender_type: 1, initiator_id: '0', content_type: 1, content: '@AI 整理任务 提取任务', created_at_unix_ms: 1, ...patch })
const deadline = (patch: Record<string, unknown> = {}) => ({ text: '', source: 'none', source_message_id: '0', reference_unix_ms: 0, timezone: 'Asia/Shanghai', resolution: 'none', reason: '', parsed_unix_ms: 0, instruction_reference_unix_ms: 0, ...patch })
const draft = (patch: Record<string, unknown> = {}) => ({ revision: '9007199254741001', title: '任务', description: '', assignee_id: '0', assignee_name: '', assignee_resolution: 'none', due_at_unix_ms: 0, source_message_id: '9007199254741011', deadline: deadline(), ...patch })
const item = (patch: Record<string, unknown> = {}) => ({ item_index: 0, status: 'waiting_confirmation', task_id: '0', reply_status: 'not_started', reply_msg_id: '', draft: draft(), ...patch })
const collection = (patch: Record<string, unknown> = {}) => ({ run_id: scope.runId, team_id: scope.teamId, group_id: scope.groupId, item_count: 1, items: [item()], ...patch })
const single = (value = item(), count = 1) => ({ run_id: scope.runId, team_id: scope.teamId, group_id: scope.groupId, item_count: count, item: value })

test('own command requires persistent ID, explicit ordinary sender metadata and full syntax', () => {
  assert.equal(isOwnAgentCommand(message(), '9007199254740999'), true)
  assert.equal(isOwnAgentCommand(message({ sender_type: 0, content: '\u0085@AI\u2003整理任务\n' + '😀'.repeat(2000) + '\u3000' }), '9007199254740999'), true)
  for (const patch of [{ id: '0' }, { id: 'pending:x' }, { id: '9223372036854775808' }, { from_id: '2' }, { sender_type: undefined }, { sender_type: 2 }, { initiator_id: undefined }, { initiator_id: '1' }, { content_type: 2 }]) assert.equal(isOwnAgentCommand(message(patch), '9007199254740999'), false)
  for (const content of ['@AI 整理任务', '@AI整理任务 工作', '@AI 整理任务工作', '@ai 整理任务 工作', '引用 @AI 整理任务 工作', '@AI 整理任务 ' + '😀'.repeat(2001), '@AI 整理任务 \ud800', '\ufeff@AI 整理任务 工作']) assert.equal(isOwnAgentCommand(message({ content }), '9007199254740999'), false)
})

test('trigger preserves large IDs and enforces scope and status/run combinations', () => {
  const raw = { message_id: triggerScope.messageId, team_id: scope.teamId, group_id: scope.groupId, status: 'completed', run_id: scope.runId }
  assert.equal(decodeAgentTrigger(raw, triggerScope)?.run_id, '9007199254740995')
  for (const status of ['queued', 'running', 'exhausted']) assert.equal(decodeAgentTrigger({ ...raw, status, run_id: '0' }, triggerScope)?.status, status)
  for (const patch of [{ team_id: '2' }, { group_id: '2' }, { message_id: '2' }, { run_id: '0' }, { run_id: 3 }, { status: 'failed' }, { status: 'queued' }, { run_id: '01' }, { run_id: '9223372036854775808' }]) assert.equal(decodeAgentTrigger({ ...raw, ...patch }, triggerScope), null)
})

test('collection and single item reject incomplete scopes, counts and noncontiguous indices', () => {
  assert.equal(decodeDraftCollection(collection(), scope)?.items[0]?.draft.revision, '9007199254741001')
  assert.equal(decodeDraftCollection(collection(), scope)?.items[0]?.draft.source_message_id, '9007199254741011')
  const items = [0, 1, 2, 3, 4].map(item_index => item({ item_index }))
  assert.equal(decodeDraftCollection(collection({ item_count: 5, items }), scope)?.items.length, 5)
  assert.equal(decodeDraftItem(single(items[4], 5), scope, 4)?.item_index, 4)
  for (const patch of [{ run_id: '2' }, { team_id: '2' }, { group_id: '2' }, { item_count: 0 }, { item_count: 6 }, { item_count: 1.5 }, { item_count: 2 }, { items: [item({ item_index: 1 })] }, { items: [item(), item()] }]) assert.equal(decodeDraftCollection(collection(patch), scope), null)
  assert.equal(decodeDraftItem(single(), scope, 1), null)
  assert.equal(decodeDraftItem({ ...single(), group_id: '2' }, scope, 0), null)
  for (const raw of [null, [], {}, 1, 'x']) { assert.equal(decodeDraftCollection(raw, scope), null); assert.equal(decodeDraftItem(raw, scope, 0), null); assert.equal(decodeAgentTrigger(raw, triggerScope), null) }
})

test('item state and deterministic reply identity follow collection HTTP contract', () => {
  for (const status of ['waiting_confirmation', 'creating', 'skipped', 'succeeded']) {
    const valid = item({ status, task_id: status === 'succeeded' ? '9007199254741021' : '0', reply_status: 'disabled' })
    assert.equal(decodeDraftItem(single(valid), scope, 0)?.status, status)
  }
  for (const reply_status of ['pending', 'accepted']) {
    assert.equal(decodeDraftItem(single(item({ status: 'succeeded', task_id: '1', reply_status, reply_msg_id: 'bot-task:9007199254740995' })), scope, 0)?.reply_status, reply_status)
    assert.ok(decodeDraftItem(single(item({ item_index: 2, status: 'succeeded', task_id: '1', reply_status, reply_msg_id: 'bot-task:9007199254740995:2' }), 3), scope, 2))
  }
  assert.ok(decodeDraftItem(single(item({ status: 'succeeded', task_id: '1', reply_status: 'unknown' })), scope, 0))
  for (const patch of [{ status: 'failed' }, { status: 'succeeded' }, { task_id: '1' }, { status: 'skipped' }, { reply_status: '' }, { reply_status: 'unknown' }, { reply_status: 'pending', reply_msg_id: 'bot-task:9007199254740995' }, { reply_msg_id: 'bogus' }, { status: 'succeeded', task_id: '1', reply_status: 'accepted', reply_msg_id: 'bot-task:9007199254740995:0' }]) assert.equal(decodeDraftItem(single(item(patch)), scope, 0), null)
})

test('assignee resolution enforces matching and rejects unresolved creating items', () => {
  for (const patch of [{ assignee_resolution: 'matched', assignee_id: '1', assignee_name: '张三' }, { assignee_resolution: 'selected', assignee_id: '1' }, { assignee_resolution: 'unassigned', assignee_name: '张三' }, ...['not_found', 'ambiguous', 'truncated'].map(assignee_resolution => ({ assignee_resolution, assignee_name: '张三' }))]) assert.ok(decodeDraftItem(single(item({ draft: draft(patch) })), scope, 0))
  for (const patch of [{ assignee_resolution: '' }, { assignee_resolution: 'matched' }, { assignee_resolution: 'none', assignee_id: '1' }, { assignee_resolution: 'selected' }, { assignee_resolution: 'not_found' }, { assignee_name: ' 张三' }, { assignee_name: '😀'.repeat(65) }, { revision: '0' }, { source_message_id: '-1' }, { title: '' }, { title: '任务 ' }, { title: '😀'.repeat(201) }, { description: '😀'.repeat(2001) }]) assert.equal(decodeDraftItem(single(item({ draft: draft(patch) })), scope, 0), null)
  assert.equal(decodeDraftItem(single(item({ status: 'creating', draft: draft({ assignee_resolution: 'ambiguous', assignee_name: '张三' }) })), scope, 0), null)
})

test('deadline metadata permits parsed, unresolved, selected and unset provenance', () => {
  const parsed = deadline({ text: '明天', source: 'message', source_message_id: '9007199254741011', reference_unix_ms: 100, parsed_unix_ms: 200, resolution: 'parsed' })
  const unresolved = deadline({ text: '明天', source: 'instruction', resolution: 'needs_input', reason: 'missing_reference' })
  for (const patch of [{ deadline: parsed, due_at_unix_ms: 200 }, { deadline: unresolved }, { deadline: { ...parsed, resolution: 'unset' } }, { deadline: { ...unresolved, resolution: 'selected' }, due_at_unix_ms: 300 }, { deadline: deadline({ resolution: 'selected' }), due_at_unix_ms: 300 }, { deadline: deadline({ instruction_reference_unix_ms: 100 }) }]) assert.ok(decodeDraftItem(single(item({ draft: draft(patch) })), scope, 0))
  for (const patch of [{ deadline: null }, { due_at_unix_ms: Number.MAX_SAFE_INTEGER + 1 }, { due_at_unix_ms: 253402300800000 }, { due_at_unix_ms: '0' }, { deadline: { ...parsed, reference_unix_ms: 0 } }, { deadline: { ...parsed, timezone: 'UTC' } }, { deadline: { ...parsed, parsed_unix_ms: 201 }, due_at_unix_ms: 200 }, { deadline: { ...unresolved, source_message_id: '1' } }, { deadline: { ...unresolved, reason: 'bogus' } }, { deadline: deadline({ resolution: 'parsed' }) }, { deadline: deadline({ reference_unix_ms: 1 }) }, { deadline: { ...parsed, text: ' 明天' } }]) assert.equal(decodeDraftItem(single(item({ draft: draft(patch) })), scope, 0), null)
  assert.equal(decodeDraftItem(single(item({ status: 'creating', draft: draft({ deadline: unresolved }) })), scope, 0), null)
})

test('canonical decimal IDs reject trailing line terminators', () => {
  for (const suffix of ['\n', '\r', '\u2028', '\u2029']) {
    assert.equal(isOwnAgentCommand(message({ id: '1' + suffix }), '9007199254740999'), false)
    assert.equal(decodeDraftItem(single(item({ draft: draft({ revision: '1' + suffix }) })), scope, 0), null)
  }
})

test('every required HTTP field must exist before any item is exposed', () => {
  for (const key of Object.keys(item())) {
    const value: Record<string, unknown> = item(); delete value[key]
    assert.equal(decodeDraftItem(single(value as ReturnType<typeof item>), scope, 0), null, key)
  }
  for (const key of Object.keys(draft())) {
    const value: Record<string, unknown> = draft(); delete value[key]
    assert.equal(decodeDraftItem(single(item({ draft: value })), scope, 0), null, key)
  }
  for (const key of Object.keys(deadline())) {
    const value: Record<string, unknown> = deadline(); delete value[key]
    assert.equal(decodeDraftItem(single(item({ draft: draft({ deadline: value }) })), scope, 0), null, key)
  }
})

test('collection rejects absent array slots rather than exposing a partial collection', () => {
  assert.equal(decodeDraftCollection(collection({ items: Array(1) }), scope), null)
})
