import { isId } from '../api/client.ts'
import type { Selection } from '../messages/directory.ts'
import type { ChatMessage } from '../messages/history.ts'
import { isOwnAgentCommand } from './model.ts'

export type AgentPanelEntry = { mode: 'ask'; teamId: string; groupId: string } | { mode: 'trigger'; teamId: string; groupId: string; messageId: string }

export function canShowAgentToolbar(selection: Selection | null): boolean {
  return !!selection && selection.kind === 'group' && selection.joined === true && isId(selection.teamId) && isId(selection.groupId)
}

export function canShowOwnAgentTrigger(selection: Selection | null, message: ChatMessage, ownId: string): boolean {
  return canShowAgentToolbar(selection) && isOwnAgentCommand(message, ownId)
}

export function agentPanelEntry(routeName: string, teamId: string, groupId: string, query: Record<string, unknown>, selection: Selection | null): AgentPanelEntry | null {
  if (routeName !== 'group' || !canShowAgentToolbar(selection) || selection?.teamId !== teamId || selection.groupId !== groupId) return null
  if (query.ai === 'ask' && query.ai_message_id === undefined) return { mode: 'ask', teamId, groupId }
  if (query.ai === undefined && isId(query.ai_message_id)) return { mode: 'trigger', teamId, groupId, messageId: query.ai_message_id }
  return null
}

const commandPrefix = '@AI 整理任务 '
export function prefillAgentCommand(draft: string): string {
  if (draft.startsWith(commandPrefix)) return draft
  if (draft === commandPrefix.trimEnd()) return commandPrefix
  return commandPrefix + draft
}
