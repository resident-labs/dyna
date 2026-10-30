// The Services page's model: the registry's services in the page's sections,
// and what each row says. The registry decides the groups and their order;
// this file only words them. Nothing here changes data.
import type { RegistryGroup, RegistryService, RegistryServiceId } from '../api/service.ts'
import type { Tone } from '../ui/tone.ts'
import { clock } from '../ui/format.ts'

export type SectionId = RegistryGroup | 'not-set-up'

export interface ServiceSection {
  id: SectionId
  title: string
  description: string
  services: RegistryService[]
}

const sectionText: Record<SectionId, { title: string; description: string }> = {
  'pull-requests': { title: 'Pull requests', description: 'Watch, review, check and land your changes.' },
  agents: { title: 'Agents', description: 'Agent sessions Dyna keeps running for you.' },
  'this-computer': { title: 'This computer', description: 'Upkeep that keeps Dyna healthy.' },
  'not-set-up': { title: 'Not set up', description: 'These have nothing to do until you set them up.' },
}

// sections puts each service in its registry group, in the registry's order,
// and moves every service that is not set up into one last section.
export function sections(registry: RegistryService[]): ServiceSection[] {
  const out: ServiceSection[] = []
  const add = (id: SectionId, service: RegistryService): void => {
    const section = out.find((s) => s.id === id)
    if (section) section.services.push(service)
    else out.push({ id, ...sectionText[id], services: [service] })
  }
  for (const s of registry) if (s.state !== 'not-set-up') add(s.group, s)
  for (const s of registry) if (s.state === 'not-set-up') add('not-set-up', s)
  return out
}

export interface Pill {
  label: string
  tone: Tone
}

// pillOf is the one colored mark a row may carry (rule r16): a failed last
// run, or a service that cannot do its work. Every other state is muted text.
export function pillOf(s: RegistryService): Pill | null {
  if (s.state === 'error') return { label: 'Last run failed', tone: 'danger' }
  if (s.detail === 'profile-waiting') return { label: 'Waiting for a profile', tone: 'warning' }
  if (s.detail === 'unreadable') return { label: 'Could not be read', tone: 'warning' }
  return null
}

// liveOf names work that is running now, which the owner cannot see anywhere
// else on the page. The row shows it as a small live dot.
export function liveOf(s: RegistryService): string {
  if (s.state !== 'running') return ''
  if (s.detail === 'in-a-call') return 'In a call'
  return s.count ? `Working · ${s.count} waiting` : 'Working'
}

// stateWord is the muted word for a quiet state. A state that a switch, a
// pill or the live dot already shows has no word, so "On" never sits beside
// a switch.
function stateWord(s: RegistryService): string {
  if (pillOf(s) || liveOf(s)) return ''
  switch (s.state) {
    case 'idle': return 'Idle'
    case 'waiting': return s.detail === 'no-run-yet' ? 'No run yet' : s.count ? `${s.count} waiting` : 'Waiting'
    case 'off': return s.detail === 'unavailable' ? 'Not available on this computer' : s.switch ? '' : 'Off'
    case 'on': return s.count ? `${s.count} watched` : ''
    default: return ''
  }
}

// when is a time as the clock time today, or as a short date before today.
export function when(iso: string, now: number): string {
  const d = new Date(iso)
  if (!Number.isFinite(d.getTime())) return ''
  return d.toDateString() === new Date(now).toDateString() ? clock(d) : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

// factsOf is the muted line of a row: a quiet state, the last and next run,
// and the profile when the service runs an agent.
export function factsOf(s: RegistryService, now: number): string[] {
  return [
    stateWord(s),
    s.lastRun ? `Last ${when(s.lastRun, now)}` : '',
    s.nextRun && s.state !== 'off' ? `Next ${when(s.nextRun, now)}` : '',
    s.profile,
  ].filter(Boolean)
}

// canRun says whether the page offers Run now: a built-in pass with a
// schedule, or the repository's update hook.
export const canRun = (s: RegistryService): boolean => !!s.schedule || (s.id === 'updates' && s.state !== 'not-set-up')

// switchHint says where to change a switch the app cannot write.
export const switchHint: Partial<Record<RegistryServiceId, string>> = {
  library: 'Turn the librarian on or off with dyna library enable or disable in its repository.',
}

// setupHint says how to set up a service that has no setting to change here.
export const setupHint: Partial<Record<RegistryServiceId, string>> = {
  'issue-watch': 'Run dyna git issue watch <issue> in this room.',
  updates: 'Add a hook named self-update in Hooks below.',
}
