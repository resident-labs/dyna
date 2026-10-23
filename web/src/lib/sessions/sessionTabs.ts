// The tabs of a session view. Media, Computer and Shell show only when there is something to see.
// computer: the session's computer, or null when computers are off.
import type { ComputerInfo } from '../api/computer.ts'
import type { Post } from '../api/room.ts'

export type SessionTabId = 'chat' | 'media' | 'computer' | 'shell'

export interface SessionTab {
  id: SessionTabId
  label: string
  count?: number
  live?: boolean
  aria?: string
}

export interface SessionTabsInput {
  media?: Pick<Post, 'attachments'>[]
  computer?: Pick<ComputerInfo, 'state'> | null
  shells?: number
  tab?: string
}

export function sessionTabs({ media = [], computer = null, shells = 0, tab = '' }: SessionTabsInput): SessionTab[] {
  const tabs: Omit<SessionTab, 'aria'>[] = [
    { id: 'chat', label: 'Messages' },
    ...(media.length ? [{ id: 'media' as const, label: 'Media', count: media.reduce((n, p) => n + (p.attachments?.length || 0), 0) }] : []),
    ...(computer ? [{ id: 'computer' as const, label: 'Computer' }] : []),
    ...(shells || tab === 'shell' ? [{ id: 'shell' as const, label: 'Shell' }] : []),
  ]
  return tabs.map((t) => ({ ...t, aria: t.count ? `${t.label}, ${t.count} items` : undefined }))
}
