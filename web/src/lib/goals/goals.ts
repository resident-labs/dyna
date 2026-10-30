import type { Goal } from '../api/goal.ts'

// TODO: legacy/shim remove later — keeps the goal tree nested for goals stored before parent was dropped from the payload; remove when no stored goal has a parent
export type GoalNode = Pick<Goal, 'id' | 'state'> & { parent?: string }
export type GoalTone = 'sage' | 'faint' | 'gold'
export type GoalFilter = 'done' | 'active'
type TreeRow<G> = { goal: G; depth: number; context: boolean }
type GoalStatusView = Pick<Goal, 'state' | 'createdAt' | 'updatedAt'> & { done?: { summary?: string } }

export function goalTree<G extends GoalNode>(goals: readonly G[], filter = 'active'): TreeRow<G>[] {
  const byId = new Map(goals.map((g): [string, G] => [g.id, g]))
  const shown = new Set(goals.filter((g) => (filter === 'done' ? ['done', 'dropped'].includes(g.state) : g.state === filter)).map((g) => g.id))
  for (const id of [...shown]) {
    let parent = byId.get(id)?.parent
    const seen = new Set([id])
    while (parent && byId.has(parent) && !seen.has(parent)) {
      seen.add(parent)
      shown.add(parent)
      parent = byId.get(parent)?.parent
    }
  }
  const out: TreeRow<G>[] = []
  const seen = new Set<string>()
  function visit(g: G, depth: number): void {
    if (seen.has(g.id)) return
    seen.add(g.id)
    if (shown.has(g.id)) out.push({ goal: g, depth, context: filter === 'done' ? !['done', 'dropped'].includes(g.state) : g.state !== filter })
    for (const child of goals.filter((c) => c.parent === g.id)) visit(child, shown.has(g.id) ? depth + 1 : depth)
  }
  for (const g of goals.filter((g) => !g.parent || !byId.has(g.parent))) visit(g, 0)
  for (const g of goals) if (!seen.has(g.id)) visit(g, 0)
  return out
}

export function goalTone(g: Pick<Goal, 'state'>): GoalTone {
  if (g.state === 'done') return 'sage'
  if (g.state === 'dropped') return 'faint'
  return 'gold'
}

export function goalAgo(at: string, now = Date.now()): string {
  const n = Math.max(0, Math.floor((now - new Date(at).getTime()) / 60000))
  if (!Number.isFinite(n)) return ''
  return n < 60 ? `${n} m ago` : n < 1440 ? `${Math.floor(n / 60)} h ago` : `${Math.floor(n / 1440)} d ago`
}

export function goalStatus(g: GoalStatusView, now = Date.now()): string {
  if (g.state === 'done') return `done${g.done?.summary ? ' · ' + g.done.summary : ''}`
  if (g.state === 'dropped') return `dropped${g.done?.summary ? ' · ' + g.done.summary : ''}`
  return `active · updated ${goalAgo(g.updatedAt ?? g.createdAt, now)}`
}

export interface GoalParts { aim: string; done: string }

// goalParts splits "<aim>; done when <condition>" so the condition reads as its own line.
export function goalParts(text = ''): GoalParts {
  const m = /[;,]?\s*\bdone when\b:?\s*/i.exec(text)
  return m ? { aim: text.slice(0, m.index).trim(), done: text.slice(m.index + m[0].length).trim() } : { aim: text, done: '' }
}
// goalFilter is the goals list filter that shows a goal.
export const goalFilter = (g?: Pick<Goal, 'state'> | null): GoalFilter => (g && ['done', 'dropped'].includes(g.state) ? 'done' : 'active')

// itemCountLabel renders a goal's item counts: "2 open · 5 items".
export function itemCountLabel(count?: readonly [number, number] | null): string {
  if (!count || !count[1]) return ''
  return `${count[0]} open · ${count[1]} ${count[1] === 1 ? 'item' : 'items'}`
}

// statusPresentation keeps ordinary computed states quiet and highlights attention.
export function statusPresentation(label: string): { label: string; tone: 'danger' | 'warning' | null } {
  const labels: Record<string, string> = { on_track: 'On track', waiting: 'Waiting', stuck: 'Stuck', errored: 'Errored', ready_to_close: 'Ready to close', unowned: 'Unowned', needs_root: 'Needs root' }
  return { label: labels[label] ?? '', tone: label === 'errored' ? 'danger' : label === 'stuck' || label === 'needs_root' ? 'warning' : null }
}
