import { clock } from '../ui/format.ts'

// The Services page's log and session lists, built from rows the app already
// loads: members (created and ended times), changes (reviewAt, verdict) and
// the goal journal's events. Each event names the session it opens.
const time = (iso) => new Date(iso).getTime()
const sessions = (members) => members.filter((m) => !m.owner && m.id)

// serviceOf names the service a session belongs to, for the page's filter.
export const serviceOf = (m) => (m.role === 'reviewer' ? 'reviewer' : m.role === 'keeper' ? 'keeper' : m.role === 'merge-review' ? 'merge' : 'agents')

// serviceEvents is every start, finish, verdict, merge trial and goal event, newest first.
export function serviceEvents({ members, changes, journal, history = [] }) {
  const all = sessions(members)
  const events = []
  for (const m of all) {
    const change = m.role === 'reviewer' ? changes.find((c) => c.reviewerSession === m.id) : null
    events.push({ key: `start:${m.id}`, ts: time(m.created), who: m.name, text: change ? `started on #${change.number}` : 'started', session: m.id, service: serviceOf(m) })
    if (m.ended) events.push({ key: `end:${m.id}`, ts: time(m.ended), who: m.name, text: m.closed ? 'closed' : 'finished', session: m.id, service: serviceOf(m) })
  }
  for (const c of changes) {
    if (!c.verdict || !c.reviewAt) continue
    events.push({ key: `verdict:${c.key ?? c.number}:${c.reviewAt}`, ts: time(c.reviewAt), who: c.reviewerName || 'reviewer', text: `gave a verdict: ${c.verdict.replaceAll('-', ' ')} on #${c.number}`, session: c.reviewerSession, service: 'reviewer' })
  }
  for (const j of journal) {
    const who = all.find((m) => m.id === j.by || m.address === j.by)
    events.push({ key: `journal:${j.id}`, ts: time(j.at), who: who?.name ?? j.by, text: `${j.event}${j.goal ? ` ${j.goal}` : ''}${j.text ? `: ${j.text}` : ''}`, session: who?.id, service: who ? serviceOf(who) : 'keeper' })
  }
  history.forEach((h, i) => {
    const n = h.changes ?? 0
    events.push({ key: `merge:${h.at}:${i}`, ts: time(h.at), who: 'merge queue', text: h.kind === 'publish' ? `published main · ${n} ${n === 1 ? 'change' : 'changes'}` : `trial ${h.outcome}${h.detail ? `: ${h.detail}` : ''}`, service: 'merge' })
  })
  return events.sort((a, b) => b.ts - a.ts)
}

// serviceSessions splits sessions into current (open, latest activity first)
// and earlier (closed, latest end first).
export function serviceSessions(members) {
  const all = sessions(members)
  return {
    current: all.filter((m) => !m.closed).sort((a, b) => time(b.lastActivity) - time(a.lastActivity)),
    earlier: all.filter((m) => m.closed).sort((a, b) => time(b.ended) - time(a.ended)),
  }
}

// when is the time of day for today's events, and the date for older ones.
export function when(ts) {
  const d = new Date(ts)
  return d.toDateString() === new Date().toDateString() ? clock(ts) : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}
