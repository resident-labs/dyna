// A rule an agent wrote. The owner's rules have the setter "owner".
export const agentRule = (r) => r.setter !== 'owner'

// The words for a scope. Rules and service pages both use them.
export const scopeLabels = { global: 'All rooms', room: 'This room' }

// A rule for all rooms. Global rule IDs start with "u", room rule IDs with "r".
export const globalRule = (r) => r.id.startsWith('u')

export function ruleFilter(rules = [], filter = 'all') {
  if (filter === 'owner') return rules.filter((r) => !agentRule(r))
  if (filter === 'agents') return rules.filter(agentRule)
  return rules
}

// "1 665 / 3 000": digit groups split by a space, as the design and the
// CLI error text write them.
const group = (n) => String(Math.max(0, Math.floor(n || 0))).replace(/\B(?=(\d{3})+(?!\d))/g, ' ')
export const ruleUsage = (view) => `${group(view?.used)} / ${group(view?.max)}`

// "2h ago", or "edited 5m ago" once the rule changed after it was added.
export function ruleTime(r, now = Date.now()) {
  const edited = r.edits?.length || (r.updatedAt && r.updatedAt !== r.createdAt)
  const at = new Date(edited ? r.updatedAt : r.createdAt).getTime()
  if (!Number.isFinite(at)) return ''
  const m = Math.max(0, Math.floor((now - at) / 60000))
  const ago = m < 1 ? 'just now' : m < 60 ? `${m}m ago` : m < 1440 ? `${Math.floor(m / 60)}h ago` : `${Math.floor(m / 1440)}d ago`
  return edited ? `edited ${ago}` : ago
}

// The "goals" event fires on every goals store write. Replace the shown
// list only when the rule set changed, and hold a new list back while an
// inline edit is open so the edit box does not jump. A list that matches
// the shown one drops any held list, so a stale one never lands later.
export function nextRules(shown, incoming, editing) {
  if (!incoming || incoming.hash === shown?.hash) return { shown, pending: null }
  return editing ? { shown, pending: incoming } : { shown: incoming, pending: null }
}
