import test from 'node:test'
import assert from 'node:assert/strict'
import { agentPanelEntry, canShowAgentToolbar, canShowOwnAgentTrigger, prefillAgentCommand } from './navigation.ts'
import type { Selection } from '../messages/directory.ts'
import type { ChatMessage } from '../messages/history.ts'

const group: Selection = { key: 'group:2:3', kind: 'group', teamId: '2', groupId: '3', title: '研发', joined: true }
const command: ChatMessage = { id: '9007199254740993', msg_id: 'saved', from_id: '5', sender_type: 1, initiator_id: '0', content_type: 1, content: '@AI 整理任务 梳理发布计划', created_at_unix_ms: 1 }

test('panel query opens only one valid mode in the joined current team group', () => {
  assert.deepEqual(agentPanelEntry('group', '2', '3', { ai: 'ask' }, group), { mode: 'ask', teamId: '2', groupId: '3' })
  assert.deepEqual(agentPanelEntry('group', '2', '3', { ai_message_id: command.id }, group), { mode: 'trigger', teamId: '2', groupId: '3', messageId: command.id })
  for (const selection of [null, { ...group, joined: false }, { ...group, groupId: '4' }, { ...group, kind: 'direct' as const }]) {
    assert.equal(agentPanelEntry('group', '2', '3', { ai: 'ask' }, selection), null)
  }
  for (const query of [{}, { ai: 'draft' }, { ai: ['ask'] }, { ai_message_id: '0' }, { ai_message_id: 'pending' }, { ai: 'ask', ai_message_id: command.id }]) assert.equal(agentPanelEntry('group', '2', '3', query, group), null)
  assert.equal(agentPanelEntry('direct', '2', '3', { ai: 'ask' }, group), null)
  assert.equal(agentPanelEntry('group', '2', '4', { ai: 'ask' }, group), null)
})

test('AI entry is limited to joined group and persisted own plain command metadata', () => {
  assert.equal(canShowAgentToolbar(group), true)
  assert.equal(canShowAgentToolbar({ ...group, joined: false }), false)
  assert.equal(canShowAgentToolbar({ ...group, kind: 'direct' }), false)
  assert.equal(canShowOwnAgentTrigger(group, command, '5'), true)
  for (const message of [{ ...command, id: 'pending:x' }, { ...command, sender_type: undefined }, { ...command, initiator_id: undefined }, { ...command, from_id: '6' }, { ...command, sender_type: 2 }, { ...command, content: '其他讨论' }]) assert.equal(canShowOwnAgentTrigger(group, message, '5'), false)
  assert.equal(canShowOwnAgentTrigger({ ...group, joined: false }, command, '5'), false)
  assert.equal(canShowOwnAgentTrigger(group, command, '6'), false)
})

test('instruction prefilling keeps existing text and never duplicates the command prefix', () => {
  assert.equal(prefillAgentCommand(''), '@AI 整理任务 ')
  assert.equal(prefillAgentCommand('发布计划'), '@AI 整理任务 发布计划')
  assert.equal(prefillAgentCommand('@AI 整理任务 发布计划'), '@AI 整理任务 发布计划')
})
