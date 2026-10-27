// What stops a session from finishing on its own, as roomsvc.Member.responsibilities
// sends it: a count for each kind (subagent.Responsibility.Kind).
export type Responsibilities = Record<string, number>

export interface Line {
  kind: string
  n: number
  label: string
}

const kinds: [kind: string, one: string, many: string][] = [
  ['pr', 'pull request', 'pull requests'],
  ['goal', 'goal', 'goals'],
  ['item', 'goal item', 'goal items'],
  ['question', 'request', 'requests'],
  ['child', 'child', 'children'],
  ['shell', 'shell', 'shells'],
  ['workflow', 'workflow', 'workflows'],
]

export function isResponsibilities(value: unknown): value is Responsibilities {
  return typeof value === 'object' && value !== null && Object.values(value).every((n) => typeof n === 'number')
}

// linesOf lists the kinds with a count, in a fixed order. A kind the web does not know follows by its name.
export function linesOf(counts: unknown): Line[] {
  if (!isResponsibilities(counts)) return []
  const known = kinds.map(([kind, one, many]) => ({ kind, n: counts[kind] ?? 0, label: (counts[kind] ?? 0) === 1 ? one : many }))
  const other = Object.keys(counts).filter((kind) => !kinds.some(([k]) => k === kind)).sort().map((kind) => ({ kind, n: counts[kind] ?? 0, label: kind }))
  return [...known, ...other].filter((line) => line.n > 0)
}

export const totalOf = (counts: unknown): number => linesOf(counts).reduce((sum, line) => sum + line.n, 0)

// summaryOf is the breakdown as one line: "1 goal, 2 shells".
export const summaryOf = (counts: unknown): string => linesOf(counts).map((line) => `${line.n} ${line.label}`).join(', ')
