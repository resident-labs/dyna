// The Services page's model: the registry's services in the page's sections,
// and what each row says. The registry decides the groups and their order;
// this file only words them. Nothing here changes data.
import type { RegistryGroup, RegistryService } from '../api/service.ts'
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

// stateWord is the muted word for a state that needs no attention, work in
// progress included. A state that a switch or a pill already shows has no
// word, so "On" never sits beside a switch.
function stateWord(s: RegistryService): string {
  if (pillOf(s)) return ''
  switch (s.state) {
    case 'running': return s.detail === 'in-a-call' ? 'In a call' : s.count ? `Working · ${s.count} waiting` : 'Working'
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
// and the profile when the service runs an agent. A switch the app cannot
// write shows as words, because a switch that never moves reads as broken.
export function factsOf(s: RegistryService, now: number, canSwitch: boolean): string[] {
  return [
    s.switch && !canSwitch ? `${s.switch.on ? 'On' : 'Off'} for this repository` : '',
    stateWord(s),
    s.lastRun ? `Last ${when(s.lastRun, now)}` : '',
    s.nextRun && s.state !== 'off' ? `Next ${when(s.nextRun, now)}` : '',
    s.profile,
  ].filter(Boolean)
}
