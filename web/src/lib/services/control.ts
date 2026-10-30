// The Services page's writes. Each names the call that owns it, so the page
// never carries its own copy of a write path. A refused write throws the
// engine's message, which the row shows inline.
import type { api } from '../../api.ts'
import type { RegistryService } from '../api/service.ts'

// Select the real browser API methods so unsupported adapters fail the type check.
export type ControlApi = Pick<typeof api, 'setReviewEnabled' | 'setMergePaused' | 'editHook' | 'editService'>

export interface ControlHost {
  room: string
  repo: string
}

// switchWrite is the call behind a service's switch, or null when the app has
// no write for it: the librarian turns on with `dyna library enable` inside
// its repository. A service run by a repository hook switches that hook, and
// every other switch belongs to a built-in pass.
export function switchWrite(api: ControlApi, host: ControlHost, s: RegistryService): ((on: boolean) => Promise<unknown>) | null {
  switch (s.id) {
    case 'reviewer': return (on) => api.setReviewEnabled(host.room, on)
    case 'merge-queue': return (on) => api.setMergePaused(host.room, !on)
    case 'library': return null
    default: return (on) => api.editService(s.id, { disabled: !on })
  }
}
