import type { Member } from '../api/room.ts'

// Persistent sessions share one status vocabulary on every human surface.
// Workflow agents, shell jobs, PRs, and goals have different state models.
const labels: Partial<Record<string, string>> = { done: 'done', error: 'failed', idle: 'idle', closed: 'closed' }

export type SessionStatusFields = Partial<Pick<Member, 'status' | 'state' | 'turns'>>

export function sessionStatus(member: SessionStatusFields = {}, withTurns = false): string {
  if (member.status === 'dormant') return 'dormant'
  const state = member.state
  const label = state === 'live'
    ? (member.status === 'queued' ? 'queued' : 'working')
    : state === undefined ? '' : (labels[state] ?? state)
  if (!withTurns) return label
  const turns = member.turns ?? 0
  if (label === 'working') return `working · turn ${turns}`
  if (state === 'done') return `done · ${turns} turn${turns === 1 ? '' : 's'}`
  if (state === 'error') return `failed turn ${turns}`
  return label
}
