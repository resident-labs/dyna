// A hook log opens in Settings, on the Hooks pane, with the run that posted the
// card already open. A feed card asks for it with one window event, so the card
// needs no route state and the session DM row can ask for it too.
export interface HookLogTarget {
  repo: string
  hook: string
  runId: string
}

export const HOOK_LOG_EVENT = 'dyna:hook-log'

export function openHookLog(target: HookLogTarget): void {
  if (typeof window === 'undefined') return
  window.dispatchEvent(new CustomEvent<HookLogTarget>(HOOK_LOG_EVENT, { detail: target }))
}

// isHookLogTarget reads the detail of that event, so the app trusts nothing it
// did not get from a card it drew.
export function isHookLogTarget(value: unknown): value is HookLogTarget {
  if (typeof value !== 'object' || value === null) return false
  const target = value as Partial<HookLogTarget>
  return typeof target.repo === 'string' && typeof target.hook === 'string' && typeof target.runId === 'string'
}

// hookLogFrom reads one window event and returns its target, or null when the
// event came from something else.
export function hookLogFrom(event: Event): HookLogTarget | null {
  if (!('detail' in event)) return null
  const detail: unknown = (event as CustomEvent<unknown>).detail
  return isHookLogTarget(detail) ? detail : null
}
