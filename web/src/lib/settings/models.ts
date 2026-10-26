// The Services group of Settings, right below Sessions where root's model is:
// every service's model and own options, so the owner sets them in one place.
// The librarian and Voice are `dyna settings` keys. The PR reviewer pin belongs
// to a room, so it is a row built from that room's service list and saved
// through the service call.
import type { SettingItem } from '../api/settings.ts'
import type { ServiceView } from '../api/service.ts'

export const REVIEWER_KEY = 'reviewer.profile'

// SERVICE_KEYS lists the rows of the group in order. A row shows only when the
// engine reports its key, so a service adds its keys here when it lands.
export const SERVICE_KEYS: readonly string[] = [REVIEWER_KEY, 'library.profile', 'voice.provider']

export const serviceTitles: Record<string, string> = {
  [REVIEWER_KEY]: 'PR reviewers model',
  'library.profile': 'Librarian model',
  'voice.provider': 'Voice provider',
}

// reviewerItem shapes the room's reviewer pin as a select setting. It is null
// when no room is open or the room has no reviewer service.
export function reviewerItem(services: readonly ServiceView[], room: string): SettingItem | null {
  const svc = services.find((s) => s.role === 'reviewer')
  if (!svc || !room) return null
  const options = ['', ...svc.profiles]
  if (svc.pinned && !svc.profiles.includes(svc.pinned)) options.push(svc.pinned)
  return {
    key: REVIEWER_KEY,
    value: svc.pinned,
    source: svc.pinned ? 'file' : 'default',
    liveVsRestart: 'next-operation',
    kind: 'select',
    options,
    optionLabels: options.map((o) => (o === '' ? 'Auto' : o === svc.pinned && !svc.profiles.includes(o) ? `${o} (not enabled)` : o)),
    help: 'The profile for PR reviewers in the room that is open now. Other rooms keep their own pin.',
  }
}
