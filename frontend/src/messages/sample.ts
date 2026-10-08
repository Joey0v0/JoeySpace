import type { ConversationSummary, MessagePreview } from './model.ts'

/** 本地界面样例；@我、未读数和消息均不是服务端状态。 */
export const conversations: readonly ConversationSummary[] = [
  { key: 'group:101:501', kind: 'group', title: '产品讨论', teamName: 'JoeySpace 团队', unreadCount: '5', mentioned: true, preview: '林悦：@你 请看一下新版消息页的阅读顺序。', updatedAt: 1760010300000 },
  { key: 'group:102:502', kind: 'group', title: '设计协作', teamName: '设计团队', unreadCount: '2', mentioned: false, preview: '陈默：我把首页的视觉稿整理好了。', updatedAt: 1760009700000 },
  { key: 'direct:9007199254740993', kind: 'direct', title: '林悦', unreadCount: '2', mentioned: false, preview: '下午方便一起核对任务状态吗？', updatedAt: 1760010000000 },
  { key: 'direct:9007199254740995', kind: 'direct', title: '陈默', unreadCount: '0', mentioned: false, preview: '明白，明天继续。', updatedAt: 1759923900000 },
]

export const messages: Readonly<Record<string, readonly MessagePreview[]>> = {
  'group:101:501': [
    { id: '801', senderName: '林悦', content: '大家早上好，新版消息页的结构已经整理好。先把需要关注的讨论放在最前面。', own: false, timeLabel: '09:34' },
    { id: '802', senderName: '你', content: '收到，我先检查会话切换和未读总览。', own: true, timeLabel: '09:36' },
    { id: '803', senderName: '任务助手', content: '已为本次讨论整理出任务草稿。后续版本会接入逐项审查入口。', own: false, timeLabel: '09:42', bot: true },
    { id: '804', senderName: '林悦', content: '@你 请看一下新版消息页的阅读顺序，尤其是群聊与私聊放在一起时是否容易分辨。', own: false, timeLabel: '10:05' },
  ],
  'group:102:502': [
    { id: '811', senderName: '陈默', content: '我把首页的视觉稿整理好了，今天可以一起看看信息层次。', own: false, timeLabel: '09:55' },
    { id: '812', senderName: '你', content: '好的，我们先确认消息列表的密度。', own: true, timeLabel: '10:02' },
  ],
  'direct:9007199254740993': [
    { id: '821', senderName: '林悦', content: '下午方便一起核对任务状态吗？', own: false, timeLabel: '10:00' },
    { id: '822', senderName: '你', content: '可以，我先把需要确认的内容列出来。', own: true, timeLabel: '10:08' },
  ],
  'direct:9007199254740995': [
    { id: '831', senderName: '陈默', content: '明白，明天继续。', own: false, timeLabel: '昨天 16:25' },
  ],
}

export function conversationPath(item: ConversationSummary): string {
  if (item.kind === 'direct') return `/messages/direct/${item.key.slice('direct:'.length)}`
  const [, teamId, groupId] = item.key.split(':')
  return `/messages/teams/${teamId}/groups/${groupId}`
}
