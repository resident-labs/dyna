// trackChanges keeps a session's changed-file count fresh without polling. It loads now and on window
// focus; poke() asks for a load when the session shows activity, and runs at most one load per `wait` ms
// (the first at once, the last one after the wait). stop() ends it. A hidden page makes no request.

// The browser parts trackChanges uses. The default is the real global object; tests pass a fake.
export interface TrackEnv {
  document: { visibilityState: string }
  Date: { now(): number }
  setTimeout(fn: () => void, ms: number): unknown
  clearTimeout(id: unknown): void
  addEventListener(name: 'focus', fn: () => void): void
  removeEventListener(name: 'focus', fn: () => void): void
}

export interface ChangeTracker {
  poke(): void
  stop(): void
}

export function trackChanges(load: () => void, wait = 2000, env: TrackEnv = globalThis): ChangeTracker {
  let last = -Infinity
  let timer: unknown = null
  const run = () => { timer = null; if (env.document.visibilityState === 'visible') { last = env.Date.now(); load() } }
  const poke = () => {
    if (timer) return
    const due = last + wait - env.Date.now()
    if (due <= 0) run()
    else timer = env.setTimeout(run, due)
  }
  env.addEventListener('focus', poke)
  run()
  return { poke, stop: () => { if (timer) env.clearTimeout(timer); timer = null; env.removeEventListener('focus', poke) } }
}

// The part of a git status that the Changes tab reads. A linked worktree has `worktree` set.
export interface ChangesStatus {
  worktree?: boolean
  files?: readonly unknown[]
}

// changedCount is what the session Changes tab counts: the changed files of a linked git worktree.
// A session in the room's main checkout has no tab, since agents never edit main.
export const changedCount = (status: ChangesStatus | null | undefined): number => (status?.worktree ? status.files?.length ?? 0 : 0)
