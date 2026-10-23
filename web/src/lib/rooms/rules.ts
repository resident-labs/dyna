// A rule an agent wrote. The owner's rules have the setter "owner".
export const agentRule = (r: RoomRule) => r.setter !== 'owner'

// The words for a scope. Rules and service pages both use them.
export const scopeLabels: Record<string, string> = { global: 'All rooms', room: 'This room' }

// A rule for all rooms. Global rule IDs start with "u", room rule IDs with "r".
export const globalRule = (r: { id: string }) => r.id.startsWith('u')

// The fields the rule rows read. A rule arrives from the host untyped.
export interface RoomRule {
  id: string
  setter: string
  setterName?: string
  text?: string
  createdAt?: string
  updatedAt?: string
  edits?: unknown[] | null
}

// The list a rules event carries: its hash and the rules themselves.
export interface RuleList {
  hash?: string
  rules?: unknown[]
}

export function ruleFilter<R extends RoomRule>(rules: R[] = [], filter = 'all'): R[] {
  if (filter === 'owner') return rules.filter((r) => !agentRule(r))
  if (filter === 'agents') return rules.filter(agentRule)
  return rules
}

// "1 665 / 3 000": digit groups split by a space, as the design and the
// CLI error text write them.
const group = (n: number | undefined) => String(Math.max(0, Math.floor(n || 0))).replace(/\B(?=(\d{3})+(?!\d))/g, ' ')
export const ruleUsage = (view?: { used?: number; max?: number } | null) => `${group(view?.used)} / ${group(view?.max)}`

// "2h ago", or "edited 5m ago" once the rule changed after it was added.
export function ruleTime(r: { createdAt?: string; updatedAt?: string; edits?: unknown[] | null }, now = Date.now()) {
  const edited = r.edits?.length || (r.updatedAt && r.updatedAt !== r.createdAt)
  const at = new Date((edited ? r.updatedAt : r.createdAt) ?? '').getTime()
  if (!Number.isFinite(at)) return ''
  const m = Math.max(0, Math.floor((now - at) / 60000))
  const ago = m < 1 ? 'just now' : m < 60 ? `${m}m ago` : m < 1440 ? `${Math.floor(m / 60)}h ago` : `${Math.floor(m / 1440)}d ago`
  return edited ? `edited ${ago}` : ago
}

// The "goals" event fires on every goals store write. Replace the shown
// list only when the rule set changed, and hold a new list back while an
// inline edit is open so the edit box does not jump. A list that matches
// the shown one drops any held list, so a stale one never lands later.
export function nextRules<L extends RuleList>(shown: L | null, incoming: L | null, editing: boolean): { shown: L | null; pending: L | null } {
  if (!incoming || incoming.hash === shown?.hash) return { shown, pending: null }
  return editing ? { shown, pending: incoming } : { shown: incoming, pending: null }
}
