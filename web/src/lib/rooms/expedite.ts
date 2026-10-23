// Goal expedite: one goal per room has priority until it ends, is stopped, or
// its expediteUntil passes. The server drops an expired value on read, so the
// web also checks the time and removes badges by itself at the deadline.
import { writable } from 'svelte/store'

interface ItemLike {
  state?: string
  assignee?: string
}
interface GoalLike {
  id: string
  state: string
  holders?: string[] | null
  shared?: boolean
  expediteUntil?: string
  items?: ItemLike[] | null
}
interface MemberLike {
  id?: string
  address?: string
  parent?: string
  role?: string
  owner?: boolean
}

// Scope is what carries the Expedited badge: the goal, the agents that do it
// with their descendants, and the PRs linked to it (by goalId).
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

// top is root or the owner: the session that leads the whole room, so its
// children are not one goal's work.
const top = (m: MemberLike): boolean => !!m.owner || m.role === 'root'

// expediteScope finds the room's expedited goal and the agents it covers.
// A goal with items hands its work to the item assignees: they and their
// children carry the badge, never the holder's other children. A goal without
// items covers the holder and its children, except for a root or owner holder.
export function expediteScope(goals: GoalLike[], members: MemberLike[], now = Date.now()): Scope {
  const g = goals.find((x) => isExpedited(x, now))
  if (!g) return none
  const holders = g.shared ? [] : g.holders ?? []
  const items = g.items ?? []
  const agents = new Set<string>()
  const add = (m: MemberLike) => {
    if (m.id && !agents.has(m.id)) {
      agents.add(m.id)
      for (const child of members) if (child.parent === m.id) add(child)
    }
  }
  const addSelf = (m: MemberLike) => {
    if (m.id) agents.add(m.id)
  }
  const assigned = (m: MemberLike): boolean =>
    items.some((i) => i.state === 'open' && !!i.assignee && (i.assignee === m.id || i.assignee === m.address))
  for (const m of members) {
    // Root or the owner leads the whole room, so it is no holder of one goal's
    // work: it carries no badge for holding it, and neither do its children.
    // As an item assignee it does that work, so it keeps the badge to itself.
    if (assigned(m)) {
      if (top(m)) addSelf(m)
      else add(m)
    } else if (holders.includes(m.id ?? '') || holders.includes(m.address ?? '')) {
      if (top(m)) continue
      if (items.length) addSelf(m)
      else add(m)
    }
  }
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
