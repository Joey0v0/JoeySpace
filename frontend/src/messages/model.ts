export interface ConversationSummary {
  readonly key: string
  readonly kind: 'group' | 'direct'
  readonly title: string
  readonly teamName?: string
  readonly unreadCount: string
  readonly mentioned: boolean
  readonly preview: string
  readonly updatedAt: number
}

export interface MessagePreview {
  readonly id: string
  readonly senderName: string
  readonly content: string
  readonly own: boolean
  readonly timeLabel: string
  readonly bot?: boolean
}

export function getUnreadConversations(
  items: readonly ConversationSummary[],
  filter: 'all' | 'mentions',
): ConversationSummary[] {
  const seen = new Set<string>()
  return items
    .filter((item) => {
      if (item.unreadCount === '0' || (filter === 'mentions' && !item.mentioned) || seen.has(item.key)) return false
      seen.add(item.key)
      return true
    })
    .sort((left, right) => Number(right.mentioned) - Number(left.mentioned) || right.updatedAt - left.updatedAt)
}

export function findConversation(
  items: readonly ConversationSummary[],
  key: string,
): ConversationSummary | undefined {
  return items.find((item) => item.key === key)
}
