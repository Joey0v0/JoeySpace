import test from 'node:test'
import assert from 'node:assert/strict'
import { findConversation, getUniqueConversations, getUnreadConversations, type ConversationSummary } from './model.ts'

const items: readonly ConversationSummary[] = [
  { key: 'direct:9007199254740993', kind: 'direct', title: '小王', unreadCount: '2', mentioned: false, preview: '收到', updatedAt: 40 },
  { key: 'group:3:8', kind: 'group', title: '工程讨论', teamName: '工程团队', unreadCount: '5', mentioned: true, preview: '请看方案', updatedAt: 20 },
  { key: 'direct:4', kind: 'direct', title: '小李', unreadCount: '0', mentioned: true, preview: '好的', updatedAt: 50 },
  { key: 'group:3:9', kind: 'group', title: '设计讨论', unreadCount: '1', mentioned: false, preview: '新版已发', updatedAt: 30 },
]

test('all excludes read conversations and prioritizes mentions without duplicate rows', () => {
  assert.deepEqual(getUnreadConversations([...items, items[0]], 'all').map((item) => item.key), ['group:3:8', 'direct:9007199254740993', 'group:3:9'])
})

test('mentions includes only unread mentioned conversations', () => {
  assert.deepEqual(getUnreadConversations(items, 'mentions').map((item) => item.key), ['group:3:8'])
})

test('repeated reads preserve source order, counts and objects', () => {
  const before = structuredClone(items)
  getUnreadConversations(items, 'all')
  getUnreadConversations(items, 'mentions')
  assert.deepEqual(items, before)
})

test('conversation keys compare as exact strings above JavaScript safe integer range', () => {
  assert.equal(findConversation(items, 'direct:9007199254740993'), items[0])
  assert.equal(findConversation(items, 'direct:9007199254740992'), undefined)
})

test('empty and unmentioned lists have no mentioned unread conversations', () => {
  assert.deepEqual(getUnreadConversations([], 'all'), [])
  assert.deepEqual(getUnreadConversations([], 'mentions'), [])
  assert.deepEqual(getUnreadConversations([items[0], items[3]], 'mentions'), [])
})

test('directory keeps one row and one unread badge count per conversation key', () => {
  const repeated = [...items, { ...items[0], title: '重复的旧预览' }, items[1]]
  const unique = getUniqueConversations(repeated)
  assert.deepEqual(unique.map((item) => item.key), items.map((item) => item.key))
  assert.equal(unique.filter((item) => item.unreadCount !== '0').length, 3)
})

test('sidebar and unread overview use the same first snapshot for conflicting duplicate keys', () => {
  const duplicate = { ...items[2], unreadCount: '3', updatedAt: 100 }
  const source = [items[2], duplicate]
  assert.deepEqual(getUniqueConversations(source), [items[2]])
  assert.deepEqual(getUnreadConversations(source, 'all'), [])
})
