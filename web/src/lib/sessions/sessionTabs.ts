// The tabs of a session view. Media, Computer, Shell, Changes and Pull requests show only when there is something to see.
// changes: files changed in the session's worktree. computer: the session's computer, or null when computers are off.
import { isOpen } from '../prs/pr.ts'
import type { ComputerInfo } from '../api/computer.ts'
import type { ChangeView } from '../api/change.ts'
import type { Post } from '../api/room.ts'

export type SessionTabId = 'chat' | 'media' | 'computer' | 'shell' | 'changes' | 'prs'

export interface SessionTab {
  id: SessionTabId
  label: string
  count?: number
  live?: boolean
  aria?: string
}

// A pull request counts as open when its state is open and its status is not untracked.
export type SessionPR = Partial<Pick<ChangeView, 'state' | 'status'>>

export interface SessionTabsInput {
  media?: Pick<Post, 'attachments'>[]
  prs?: SessionPR[]
  computer?: Pick<ComputerInfo, 'state'> | null
  changes?: number
  shells?: number
  tab?: string
}

export function sessionTabs({ media = [], prs = [], computer = null, changes = 0, shells = 0, tab = '' }: SessionTabsInput): SessionTab[] {
  const openPRs = prs.filter(isOpen).length
  const tabs: Omit<SessionTab, 'aria'>[] = [
    { id: 'chat', label: 'Messages' },
    ...(media.length ? [{ id: 'media' as const, label: 'Media', count: media.reduce((n, p) => n + (p.attachments?.length || 0), 0) }] : []),
    ...(computer ? [{ id: 'computer' as const, label: 'Computer' }] : []),
    ...(shells || tab === 'shell' ? [{ id: 'shell' as const, label: 'Shell' }] : []),
    ...(changes ? [{ id: 'changes' as const, label: 'Changes', count: changes }] : []),
    ...(prs.length ? [{ id: 'prs' as const, label: 'Pull requests', count: openPRs }] : []),
  ]
  return tabs.map((t) => ({ ...t, aria: t.count ? `${t.label}, ${t.count}${t.id === 'prs' ? ' open' : t.id === 'changes' ? ' files' : ' items'}` : undefined }))
}
