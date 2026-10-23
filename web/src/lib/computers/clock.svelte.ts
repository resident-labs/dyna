// A shared one-second clock, so every "4 s ago" on screen ticks together.
export const clock: { now: number } = $state({ now: Date.now() })
// unref: a node test that loads this module must be able to exit.
if (typeof setInterval === 'function') setInterval(() => (clock.now = Date.now()), 1000).unref?.()
