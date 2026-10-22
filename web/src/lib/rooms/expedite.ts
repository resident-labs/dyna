// Goal expedite: one goal per room has priority until it ends, is stopped, or
// its expediteUntil passes. The server drops an expired value on read, so the
// web also checks the time and removes badges by itself at the deadline.
import { writable } from 'svelte/store'

interface GoalLike {
  id: string
  state: string
  holders?: string[] | null
  shared?: boolean
  expediteUntil?: string
}
interface MemberLike {
  id?: string
  address?: string
  parent?: string
}

// Scope is what carries the Expedited badge: the goal, the agents that hold
// it with their descendants, and the PRs linked to it (by goalId).
export interface Scope {
  goalId: string
  until: number
  agents: Set<string>
}

export const none: Scope = { goalId: '', until: 0, agents: new Set() }

// The shared scope of the open room. App sets it; badges read it.
export const expedite = writable<Scope>(none)

// isExpedited is true for an open goal whose expediteUntil is still ahead.
export function isExpedited(g: GoalLike | null | undefined, now = Date.now()): boolean {
  if (!g || g.state !== 'active' || !g.expediteUntil) return false
  const until = Date.parse(g.expediteUntil)
  return until > now
}

// expediteScope finds the room's expedited goal and the agents it covers.
export function expediteScope(goals: GoalLike[], members: MemberLike[], now = Date.now()): Scope {
  const g = goals.find((x) => isExpedited(x, now))
  if (!g) return none
  const holders = g.shared ? [] : g.holders ?? []
  const agents = new Set<string>()
  const add = (m: MemberLike) => {
    if (m.id && !agents.has(m.id)) {
      agents.add(m.id)
      for (const child of members) if (child.parent === m.id) add(child)
    }
  }
  for (const m of members) if (holders.includes(m.id ?? '') || holders.includes(m.address ?? '')) add(m)
  return { goalId: g.id, until: Date.parse(g.expediteUntil as string), agents }
}

export const goalExpedited = (s: Scope, g: { id: string } | null | undefined): boolean => !!g && !!s.goalId && s.goalId === g.id
export const agentExpedited = (s: Scope, m: { id?: string } | null | undefined): boolean => !!m?.id && s.agents.has(m.id)
export const prExpedited = (s: Scope, pr: { goalId?: string } | null | undefined): boolean => !!pr?.goalId && s.goalId === pr.goalId

// heldGoals lists the open goals an agent holds. An agent with none holds
// those of its nearest ancestor that has any.
export function heldGoals<G extends GoalLike>(m: MemberLike, members: MemberLike[], goals: G[]): G[] {
  const seen = new Set<string>()
  for (let at: MemberLike | undefined = m; at && !seen.has(at.id ?? ''); at = members.find((x) => x.id === at?.parent)) {
    seen.add(at.id ?? '')
    const held = goals.filter((g) => g.state === 'active' && !g.shared && (g.holders ?? []).some((h) => h === at?.id || h === at?.address))
    if (held.length) return held
  }
  return []
}

// expediteAction is what a button for a goal does.
export const expediteAction = (s: Scope, g: { id: string }): 'stop' | 'replace' | 'start' =>
  goalExpedited(s, g) ? 'stop' : s.goalId ? 'replace' : 'start'
