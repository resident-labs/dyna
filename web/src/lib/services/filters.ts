// The Services page's filters: by service, by session state and by time range.
// They work on the rows already loaded; the page's 200-row caps still apply after them.
export type ServiceId = 'all' | 'reviewer' | 'merge' | 'keeper' | 'agents'
export type StateFilter = 'any' | 'running' | 'idle' | 'closed'
export type Range = 'hour' | 'day' | 'week' | 'all'

export interface Filters { service: ServiceId; state: StateFilter; range: Range }
export const defaultFilters: Filters = { service: 'all', state: 'any', range: 'day' }

export const serviceOptions: { id: ServiceId; label: string }[] = [
  { id: 'all', label: 'All services' }, { id: 'reviewer', label: 'PR reviewers' }, { id: 'merge', label: 'Merge queue' }, { id: 'keeper', label: 'Goal keeper' }, { id: 'agents', label: 'Agents' },
]
export const stateOptions: { id: StateFilter; label: string }[] = [
  { id: 'any', label: 'Any state' }, { id: 'running', label: 'Running' }, { id: 'idle', label: 'Idle' }, { id: 'closed', label: 'Closed' },
]
export const rangeOptions: { id: Range; label: string }[] = [
  { id: 'hour', label: 'Last hour' }, { id: 'day', label: 'Last 24 hours' }, { id: 'week', label: 'Last 7 days' }, { id: 'all', label: 'All time' },
]
const span: Record<Range, number> = { hour: 3600000, day: 86400000, week: 7 * 86400000, all: Infinity }

export interface Evented { ts: number; service: string }
export interface Sessioned { role?: string; state?: string; closed?: boolean }

export const filterEvents = <T extends Evented>(events: T[], f: Filters, now: number): T[] =>
  events.filter((e) => (f.service === 'all' || e.service === f.service) && now - e.ts <= span[f.range])

const serviceOfSession = (m: Sessioned): ServiceId => (m.role === 'reviewer' ? 'reviewer' : m.role === 'keeper' ? 'keeper' : m.role === 'merge-review' ? 'merge' : 'agents')
const stateOfSession = (m: Sessioned): StateFilter => (m.closed ? 'closed' : m.state === 'live' ? 'running' : 'idle')

export const filterSessions = <T extends Sessioned>(sessions: T[], f: Filters): T[] =>
  sessions.filter((m) => (f.service === 'all' || serviceOfSession(m) === f.service) && (f.state === 'any' || stateOfSession(m) === f.state))
