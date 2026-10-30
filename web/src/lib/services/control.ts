// The Services page's writes. Each names the call that owns it, so the page
// never carries its own copy of a write path. A refused write throws the
// engine's message, which the row shows inline.
import type { api } from '../../api.ts'
import type { RegistryService, RegistryServiceId } from '../api/service.ts'
import type { HookRun } from '../api/settings.ts'

// Select the real browser API methods so unsupported adapters fail the type check.
export type ControlApi = Pick<typeof api, 'setReviewEnabled' | 'setMergePaused' | 'runHook' | 'editHook' | 'runService' | 'editService' | 'resetService'>

export interface ControlHost {
  room: string
  repo: string
}

// The repository hook behind the Updates service.
const updateHook = 'self-update'

// switchWrite is the call behind a service's switch, or null when the app has
// no write for it: the librarian turns on with `dyna library enable` inside
// its repository. Every other switch belongs to a built-in pass.
export function switchWrite(api: ControlApi, host: ControlHost, id: RegistryServiceId): ((on: boolean) => Promise<unknown>) | null {
  switch (id) {
    case 'reviewer': return (on) => api.setReviewEnabled(host.room, on)
    case 'merge-queue': return (on) => api.setMergePaused(host.room, !on)
    case 'updates': return (on) => api.editHook(host.repo, updateHook, { disabled: !on })
    case 'library': return null
    default: return (on) => api.editService(id, { disabled: !on })
  }
}

// runNow runs one pass of a service and resolves with its run.
export const runNow = (api: ControlApi, host: ControlHost, s: RegistryService): Promise<HookRun> =>
  s.id === 'updates' ? api.runHook(host.repo, updateHook) : api.runService(s.id)
