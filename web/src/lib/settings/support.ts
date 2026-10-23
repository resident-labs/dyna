// Harness support: what Dyna can read from a profile's harness output.
// The Go side (configsvc) computes `p.support` from the adapter table. These
// helpers only shape it for the profile list, the editor and the brief.

// Mirrors configsvc.SupportItem.
export type SupportItem = { key: string; label: string; state: string; reason: string; chip: string }

// Mirrors health.Status.
export type HealthStatus = 'unknown' | 'checking' | 'healthy' | 'unhealthy' | 'output_not_understood' | 'unverified_since_update' | 'unverified_since_edit'

// Mirrors health.Observed, which flattens health.Observation.
export type Observed = {
  running: boolean
  at: string
  successful: boolean
  stdoutSeen: boolean
  recognizedRecords: number
  textSeen: boolean
  toolSeen: boolean
  positiveUsageSeen: boolean
  lastTurnAt: string
  recognizedOutput: boolean
  probe: boolean
  probePassed: boolean
}

// Mirrors configsvc.HarnessSupport, the fields the helpers read.
export type HarnessSupport = {
  installed: boolean
  installable: boolean
  binary: string
  version: string
  versionError: string
  lastHealthyVersion: string
  dialect: string
  partial: boolean
  status: HealthStatus
  reason: string
  items: SupportItem[]
  observed?: Observed
}

// The helpers accept any subset of the support fields, so a partial view is the input type.
export type SupportView = Partial<Omit<HarnessSupport, 'observed'>> & { observed?: Partial<Observed> }

// A profile row or editor view. Every field may be missing, as the helpers allow.
export type ProfileView = { name?: string; harness?: string; disabled?: boolean; support?: SupportView }

// The fields the reviewer rule reads from a profile.
export type RankedProfile = { name: string; taste: number; cost: number; intelligence: number; disabled?: boolean }

const stateWord: Record<string, string> = { yes: 'Yes', no: 'No', unavailable: 'Unavailable' }

export const stateLabel = (state: string): string => stateWord[state] ?? state

// gaps lists the capabilities that are not fully supported.
export const gaps = (p: ProfileView | null | undefined): SupportItem[] => (p?.support?.items ?? []).filter((it) => it.state !== 'yes')

// supportChip is the one short label a profile row shows, or '' for full
// support. A second gap shows as "+1" so the row stays one chip wide.
export function supportChip(p: ProfileView | null | undefined): string {
  const g = gaps(p)
  const first = g[0]
  if (!first) return ''
  return g.length === 1 ? first.chip : `${first.chip} +${g.length - 1}`
}

export const supportTitle = (p: ProfileView | null | undefined): string => gaps(p).map((it) => `${it.label}: ${it.reason}`).join('\n')

// bestProfile picks the profile for a support session: enabled profiles, best
// taste + cost, then intelligence, then name. This is the ranking roles use
// when they choose a solution; reviewers rank by diagnostics instead.
export function bestProfile(profiles: RankedProfile[] | undefined): string {
  const ok = (profiles ?? []).filter((p) => !p.disabled)
  ok.sort((a, b) => (b.taste + b.cost) - (a.taste + a.cost) || b.intelligence - a.intelligence || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0))
  return ok[0]?.name ?? ''
}

// supportBrief is the editable brief for a session that closes the gaps.
export function supportBrief(p: { name: string; harness: string; support?: SupportView }): string {
  const s = p.support ?? {}
  const lines = gaps(p).map((it) => `- ${it.label}: ${it.state}. ${it.reason}.`)
  return [
    `Add full Dyna support for the ${p.harness} harness. Profile: ${p.name}. Output dialect: ${s.dialect ?? 'unknown'}.`,
    '',
    'Gaps today:',
    ...lines,
    '',
    'Do this in order:',
    `1. Run ${p.harness} headless on a small task. Capture a real output sample. Keep it in $DYNA_SCRATCH.`,
    '2. Extend the adapter in internal/workeroutput so it decodes what the sample shows. Update the Capabilities table.',
    '3. Add fixtures from the sample to the conformance test.',
    '4. Run the affected tests, go vet and gofmt directly.',
    '5. Open a PR with a plain-English body. Do not merge it.',
  ].join('\n')
}

// supportDraft is the room draft that starts the session: "/<profile> <brief>".
export const supportDraft = (profile: string, brief: string): string => `/${profile} ${brief.trim()}`

// ---- Harness health: what real turns and probes showed ----

const cliName = (p: ProfileView | null | undefined): string => {
  const bin = (p?.support?.binary || '').split('/').pop()
  return bin || p?.harness || 'CLI'
}

const lower = (text: string): string => text.charAt(0).toLowerCase() + text.slice(1)

export type HealthChip = { text: string; tone: 'bad' | 'warn'; title: string }

// healthChip is the one short status for a profile row: {text, tone, title}
// or null when all is well. A health problem outranks a partial-support gap.
// tone is "bad" or "warn". Health is best effort, so unknown shows nothing.
export function healthChip(p: ProfileView | null | undefined): HealthChip | null {
  const s = p?.support
  if (!s || p.disabled) return null
  const title = s.reason || ''
  if (s.installed === false && s.installable) return null // Dyna installs it on first use
  if (s.installed === false) return { text: 'Harness not installed', tone: 'bad', title: s.versionError || title }
  switch (s.status) {
    case 'output_not_understood': return { text: 'Output not understood', tone: 'bad', title }
    case 'unverified_since_update': return { text: s.version ? `Not verified since ${cliName(p)} ${s.version}` : 'Not verified since update', tone: 'warn', title }
    case 'unverified_since_edit': return { text: 'Not verified since edit', tone: 'warn', title }
    case 'unhealthy': return { text: 'Last check failed', tone: 'bad', title }
  }
  const chip = supportChip(p)
  return chip ? { text: `Partly supported: ${lower(chip)}`, tone: 'warn', title: supportTitle(p) } : null
}

// A Go zero time means "never".
const seen = (iso: string | undefined): iso is string => !!iso && !iso.startsWith('0001-')

// ago says how long since an ISO time, in plain words.
export function since(iso: string | undefined, now = Date.now()): string {
  if (!seen(iso)) return ''
  const sec = Math.max(0, Math.round((now - Date.parse(iso)) / 1000))
  if (sec < 60) return 'just now'
  if (sec < 3600) return `${Math.floor(sec / 60)} min ago`
  if (sec < 86400) return `${Math.floor(sec / 3600)} h ago`
  return `${Math.floor(sec / 86400)} d ago`
}

// observedRows lists what real turns showed, as [label, value] pairs.
export function observedRows(p: ProfileView | null | undefined, now = Date.now()): [string, string][] {
  const o = p?.support?.observed
  const last = since(o?.lastTurnAt, now) || (seen(o?.at) && !o?.probe ? since(o?.at, now) : '')
  const yes = (v: boolean | undefined): string => (!o ? 'Not yet' : v ? 'Yes' : 'No')
  const latest = !seen(o?.at) ? 'None yet' : `${o?.probe ? 'Test' : 'Real turn'}, ${since(o?.at, now)}`
  return [
    ['Last real turn', last || 'None yet'],
    ['Latest check', latest],
    ['Tool calls seen', yes(o?.toolSeen)],
    ['Token counts seen', yes(o?.positiveUsageSeen)],
  ]
}

// driftText says whether the CLI version changed since Dyna last saw it work.
export function driftText(p: ProfileView | null | undefined): string {
  const s = p?.support
  if (!s?.version) return s?.versionError ? `Version unknown: ${s.versionError}` : 'Version unknown'
  if (s.status === 'unverified_since_update') {
    return s.lastHealthyVersion ? `Updated from ${s.lastHealthyVersion}. Not verified since.` : 'Updated. Not verified since.'
  }
  if (s.lastHealthyVersion && s.lastHealthyVersion === s.version) return 'Same version as the last good check'
  return 'No version change seen'
}

// probeRows turns a finished check into one plain-words row per capability.
// state is "yes", "no" or "na" (the harness does not report it).
export type ProbeRow = { key: string; label: string; state: 'yes' | 'no' | 'na'; words: string }

export function probeRows(check: ProfileView | null | undefined): ProbeRow[] {
  const sup = check?.support
  const o = sup?.observed
  if (!sup || !o?.probe) return []
  const item = (key: string): SupportItem | undefined => (sup.items ?? []).find((it) => it.key === key)
  const rows: ProbeRow[] = [{ key: 'text', label: 'Reply text', state: o.textSeen ? 'yes' : 'no', words: o.textSeen ? 'Dyna read the reply' : 'Dyna could not read a reply' }]
  const tools = item('toolEvents')
  rows.push(tools?.state === 'yes'
    ? { key: 'tools', label: 'Tool calls', state: o.toolSeen ? 'yes' : 'no', words: o.toolSeen ? 'Dyna saw the tool call' : 'The test used a tool, but Dyna saw no tool call' }
    : { key: 'tools', label: 'Tool calls', state: 'na', words: 'This harness does not report tool calls' })
  const usage = item('usage')
  rows.push(usage?.state === 'yes'
    ? { key: 'usage', label: 'Token counts', state: o.positiveUsageSeen ? 'yes' : 'no', words: o.positiveUsageSeen ? 'Dyna saw token counts' : 'Dyna saw no token counts' }
    : { key: 'usage', label: 'Token counts', state: 'na', words: 'This harness does not report token counts' })
  return rows
}

export const PROBE_LIMIT_MS = 130000

// A timer handle: Node returns a Timeout object, browsers return a number.
export type TimerHandle = ReturnType<typeof setTimeout> | number

export type ProbeOptions = {
  limitMs?: number
  setTimer?: (fn: () => void, ms: number) => TimerHandle
  clearTimer?: (handle?: TimerHandle) => void
}

export type ProbeResult<T> =
  | { phase: 'done'; check: T }
  | { phase: 'failed'; error: string }
  | { phase: 'timeout' }

// runProbe calls the live probe and waits at most limitMs. The Go side stops
// at two minutes; the extra time only covers slow startup. It never throws:
// the result is {phase: 'done', check} (check is whatever call resolves to) | {phase: 'failed', error}
// | {phase: 'timeout'}. The timer is injectable for tests.
export async function runProbe<T>(
  call: () => Promise<T>,
  { limitMs = PROBE_LIMIT_MS, setTimer = setTimeout, clearTimer = clearTimeout }: ProbeOptions = {},
): Promise<ProbeResult<T>> {
  let timer: TimerHandle | undefined
  const limit = new Promise<ProbeResult<T>>((resolve) => { timer = setTimer(() => resolve({ phase: 'timeout' }), limitMs) })
  const run = Promise.resolve().then(call).then(
    (check): ProbeResult<T> => ({ phase: 'done', check }),
    (e: unknown): ProbeResult<T> => ({ phase: 'failed', error: String(messageOf(e) || e) }),
  )
  try {
    return await Promise.race([run, limit])
  } finally {
    clearTimer(timer)
  }
}

// messageOf reads a thrown value's message property, as e?.message does. Any
// truthy value counts, so the caller decides how to show it.
const messageOf = (e: unknown): unknown =>
  (typeof e === 'object' && e !== null) || typeof e === 'function' ? Reflect.get(e, 'message') : undefined

// ---- Session notice ----

// unreadableOutput is true when a running session's profile produced stdout
// that Dyna cannot read. Only the live turn counts: a finished check is not
// news for a session that is idle.
export const unreadableOutput = (member: { state?: string } | null | undefined, check: ProfileView | null | undefined): boolean =>
  !!member && member.state === 'live' && check?.support?.status === 'output_not_understood' && !!check.support.observed?.running

export const unreadableNotice = 'Dyna cannot read this session’s output.'
