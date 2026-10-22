import type { ServiceView } from '../api/service.ts'

export type ServiceStatus = { text: string; tone: '' | 'coral' }

// The status line of a service header: the profile the next turn uses, why,
// and how many of its sessions are open. With no usable profile it shows the
// reason alone, as a warning.
export function serviceStatus(service: ServiceView | null): ServiceStatus {
  if (!service) return { text: '', tone: '' }
  if (!service.profile) return { text: service.reason || 'No enabled profile', tone: 'coral' }
  const sessions = `${service.sessions} ${service.sessions === 1 ? 'session' : 'sessions'}`
  return { text: [service.profile, service.reason, sessions].filter(Boolean).join(' · '), tone: '' }
}

// A pin shows as "(not enabled)" when the service cannot run it. profiles
// is the service's own list of names, services-only profiles included.
export const pinNotEnabled = (pinned: string, profiles: readonly string[]): boolean => !!pinned && !profiles.includes(pinned)

// Box the thrown value so a function or a primitive can still carry message.
export function errorText(error: unknown): string {
  if (error == null) return String(error)
  const boxed: object = Object(error)
  const message: unknown = Reflect.get(boxed, 'message')
  return String(message || error)
}

// pickProfile saves the select's value. On failure it puts the select back
// on the saved pin, so the menu never shows a choice that was not saved, and
// returns the error text.
export async function pickProfile(
  select: { value: string },
  save: (profile: string) => Promise<unknown>,
  pinned: string,
): Promise<string> {
  try {
    await save(select.value)
    return ''
  } catch (error) {
    select.value = pinned
    return errorText(error)
  }
}
