// The Services page's controls. Each control names the call it uses, so the
// page never carries its own copy of a write path.

export interface ControlHost {
  room: string
  repo: string
}

export interface ControlApi {
  setReviewEnabled(room: string, enabled: boolean): Promise<unknown>
  setMergePaused(room: string, paused: boolean): Promise<unknown>
  setServiceProfile(room: string, role: string, profile: string): Promise<unknown>
  setSetting(key: string, value: string): Promise<unknown>
  enableLibrarian(repo: string, ref: string, cwd: string): Promise<unknown>
  disableLibrarian(repo: string): Promise<unknown>
  editHook(repo: string, name: string, patch: Record<string, unknown>): Promise<unknown>
  runHook(repo: string, name: string): Promise<unknown>
}

// applyControl writes one control and resolves when the engine has it. It
// throws the engine's message, which the row shows inline.
export async function applyControl(api: ControlApi, host: ControlHost, id: string, value: string | boolean): Promise<unknown> {
  const on = value === true
  const name = String(value)
  switch (id) {
    case 'review.enabled':
      return api.setReviewEnabled(host.room, on)
    case 'review.profile':
      return api.setServiceProfile(host.room, 'reviewer', name)
    case 'merge.paused':
      return api.setMergePaused(host.room, !on)
    case 'steward.profile':
      return api.setServiceProfile(host.room, 'merge-steward', name)
    case 'keeper.enabled':
      return api.setSetting('goals.enabled', on ? 'true' : 'false')
    case 'keeper.profile':
      return api.setServiceProfile(host.room, 'keeper', name)
    case 'library.enabled':
      return on ? api.enableLibrarian(host.repo, host.room, host.repo) : api.disableLibrarian(host.repo)
    case 'library.profile':
      return api.setSetting('library.profile', name)
    case 'update.enabled':
      return api.editHook(host.repo, 'self-update', { Disabled: !on })
    case 'update.run':
      return api.runHook(host.repo, 'self-update')
  }
  throw new Error(`unknown control ${id}`)
}
