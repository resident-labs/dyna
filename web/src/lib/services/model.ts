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
  if (s.detail === 'update-source-invalid') return { label: 'Source checkout is missing or invalid', tone: 'danger' }
  if (s.detail === 'multiple-update-hooks') return { label: 'Several update hooks; choose a source', tone: 'warning' }
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

// switchHint says where to change a switch the app cannot write.
export const switchHint: Partial<Record<RegistryServiceId, string>> = {
  library: 'Turn the librarian on or off with dyna library enable or disable in its repository.',
}

// setupHint says how to set up a service that has no setting to change here.
export const setupHint: Partial<Record<RegistryServiceId, string>> = {
  'issue-watch': 'Run dyna git issue watch <issue> in this room.',
  updates: 'Run dyna git hooks add --recipe self-update in this repository.',
}

// settingText names the settings keys the registry lists, in owner words; a
// key must never show as itself (rule r8).
const settingText: Record<string, { title: string; description?: string }> = {
  'pr.outsideComments': { title: 'Comments from outside the room', description: 'Deliver passes them to the author as untrusted text; hold keeps them until you admit them' },
  'pr.trustedBots': { title: 'Trusted review bots', description: 'GitHub bot logins whose comments are trusted, as a JSON list' },
  'admission.mode': { title: 'Test run limits' },
  'admission.maxAgents': { title: 'Most agents at once' },
  'lane.local.repos': { title: 'Repositories on the local lane', description: 'Absolute paths as a JSON list; Dyna fetches them every five minutes' },
  'library.enabled': { title: 'Repositories with a librarian', description: 'Absolute paths as a JSON list' },
  'library.profile': { title: 'Librarian model', description: 'Auto picks from the enabled service profiles' },
  'library.smellProfile': { title: 'Model for smell checks' },
  'library.smellsPerDay': { title: 'Most smells filed a day', description: 'Zero means no limit' },
  'library.readmeMaxLines': { title: 'Longest README, in lines' },
  'sessions.checkInInterval': { title: 'Check in every', description: 'Zero turns check-ins off' },
  'voice.agent': { title: 'Let agents use voice', description: 'Their calls spend the voice provider’s quota' },
  'voice.ducking': { title: 'Lower Voice while I talk' },
  'voice.provider': { title: 'Voice provider' },
  'voice.model': { title: 'Voice model', description: 'Empty uses the provider’s default' },
  'voice.url': { title: 'Voice server address', description: 'Needed for a self-hosted server' },
  'updater.autoCheck': { title: 'Check for updates', description: 'At most once a day' },
  'doctor.condition': { title: 'When to examine this computer', description: 'Such as load>2/core for 5m or memory>85%; empty uses the default' },
  'runs.retention': { title: 'Keep finished runs', description: 'Off keeps them forever' },
  'worktrees.retention': { title: 'Keep workflow worktrees', description: 'Changed ones, all or none, after an agent ends' },
  'worktrees.minFreeBytes': { title: 'Clean up below this free space', description: 'Zero turns this check off' },
  'worktrees.root': { title: 'Folder for new worktrees' },
}

// settingLabel is a settings key's title and description. An unknown key
// takes the first sentence of its help.
export const settingLabel = (key: string, help: string): { title: string; description?: string } =>
  settingText[key] ?? { title: (help.split(/\.\s/)[0] ?? key).replace(/\.$/, '') }

// seconds reads a time such as 30s, 90m, 1h30m or 14d as whole seconds, or null.
export function seconds(text: string): number | null {
  const m = /^\s*(?:(\d+)d)?\s*(?:(\d+)h)?\s*(?:(\d+)m)?\s*(?:(\d+)s)?\s*$/.exec(text)
  if (!m || !(m[1] || m[2] || m[3] || m[4])) return null
  return Number(m[1] ?? 0) * 86400 + Number(m[2] ?? 0) * 3600 + Number(m[3] ?? 0) * 60 + Number(m[4] ?? 0)
}
