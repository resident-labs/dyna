// Tests that slice App.svelte functions run them in one flat vm state. The open
// screen's state now lives in lib/app/router.svelte.ts, so those functions write
// through `router`. This shim gives the flattened state the same writable
// properties, and the state stays the single source of truth for assertions.

/** The screen state App reads through router. */
export const SCREEN_STATE = ['roomId', 'view', 'drill', 'sessionTurn', 'openRun', 'openJob', 'reviewOpen', 'reviewTab', 'reviewVersion', 'readOnlyDm', 'prLink', 'routing', 'routeFix', 'detailRequest'] as const

/** installRouterShim adds `router` to a vm test state, over that state's own fields. */
export function installRouterShim(state: object): void {
  const shim: Record<string, unknown> = {}
  for (const name of SCREEN_STATE) {
    Object.defineProperty(shim, name, {
      enumerable: true,
      get: () => Reflect.get(state, name),
      set: (value: unknown) => { Reflect.set(state, name, value) },
    })
  }
  Reflect.set(state, 'router', shim)
  Reflect.set(state, 'settingsPanel', { closeRoomSettings: () => { Reflect.set(state, 'showRoomSettings', false) } })
}
