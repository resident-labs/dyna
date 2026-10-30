// The Services page's controls. Each control names the call it uses, so the
// page never carries its own copy of a write path.

export interface ControlHost {
  room: string
  repo: string
}

import type { api } from '../../api.js'

// Select the real browser API methods so unsupported adapters fail the type check.
export type ControlApi = Pick<typeof api, 'setReviewEnabled' | 'setMergePaused' | 'runHook' | 'editHook' | 'editService' | 'resetService'>

// applyControl writes one control and resolves when the engine has it. It
// throws the engine's message, which the row shows inline.
export async function applyControl(api: ControlApi, host: ControlHost, id: string, value: string | boolean): Promise<unknown> {
  const on = value === true
  switch (id) {
    case 'review.enabled':
      return api.setReviewEnabled(host.room, on)
    case 'merge.paused':
      return api.setMergePaused(host.room, on)
    // A hook switch keeps the hook's settings and stops its automatic runs.
    case 'update.enabled':
      return api.editHook(host.repo, 'self-update', { disabled: !on })
    case 'update.run':
      return api.runHook(host.repo, 'self-update')
  }
  // A built-in service's controls name the service after a colon.
  const [control, service = ''] = id.split(':')
  switch (control) {
    case 'service.enabled':
      return api.editService(service, { disabled: !on })
    case 'service.every':
      return api.editService(service, { everyMinutes: Number(value) })
    case 'service.timeout':
      return api.editService(service, { timeout: String(value) })
    case 'service.reset':
      return api.resetService(service)
  }
  throw new Error(`unknown control ${id}`)
}
