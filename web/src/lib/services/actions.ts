// What a service control does. Each control id maps to an existing engine
// call; a setting id is the settings key itself.
export interface ControlApi {
  setReviewEnabled(room: string, enabled: boolean): Promise<unknown>
  setServiceProfile(room: string, role: string, profile: string): Promise<unknown>
  setSetting(key: string, value: string): Promise<unknown>
}

export function applyControl(api: ControlApi, room: string, id: string, value: string | boolean): Promise<unknown> {
  if (id === 'review.enabled') return api.setReviewEnabled(room, value === true)
  if (id.startsWith('profile.')) return api.setServiceProfile(room, id.slice('profile.'.length), String(value))
  return api.setSetting(id, String(value))
}
