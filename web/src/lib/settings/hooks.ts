// Plain-English labels and run ordering for the hook lists.
import type { Hook, HookRun } from '../api/settings.ts'
import { minutesText } from '../ui/format.ts'

const events: Record<string, string> = {
  'worktree-created': 'Worktree created',
  'before-land': 'Before land',
  'after-land': 'After land',
  'pr-merged': 'PR merged',
  'main-updated': 'Main updated',
  'main-moved': 'Main updated',
  'session-finish': 'Session finish',
  'session-turn-ended': 'Session turn ended',
  schedule: 'Schedule',
}

export function eventLabel(event: string, everyMinutes = 0): string {
  const label = events[event] ?? event
  return event === 'schedule' && everyMinutes > 0 ? `${label} · every ${minutesText(everyMinutes)}` : label
}

export function failLabel(onFail: string): string {
  return onFail === 'block' ? 'Block' : 'Alert'
}

// serviceTriggerBuiltins names the built-in hooks that wake a service. Their
// interval and threshold are the service's own settings. An owner's own hook
// is never one of them, whatever its name.
const serviceTriggerBuiltins: readonly string[] = ['doctor']

// listedHooks is what a Hooks screen lists: every hook of a repository, and on
// the global screen every hook except the service triggers.
export const listedHooks = <H extends Pick<Hook, 'builtin'>>(hooks: H[], scope: HookScope): H[] =>
  scope === 'global' ? hooks.filter((h) => !h.builtin || !serviceTriggerBuiltins.includes(h.builtin)) : hooks

// The two hook lists: one repository's hooks, or this computer's.
export type HookScope = 'repository' | 'global'

// authorLabel names who added a hook. A stored hook may record the owner, the
// engine, or the session that asked for it, and a card shows a name for each
// instead of a raw session ID.
export function authorLabel(addedBy: string | undefined): string {
  if (!addedBy) return 'someone'
  if (addedBy === 'owner') return 'you'
  if (addedBy === 'engine') return 'Dyna'
  if (addedBy.startsWith('ses_')) return 'a session'
  return addedBy
}

// Runs come oldest first. The pane shows newest first.
export function newestFirst<R extends Pick<HookRun, 'startedAt'>>(runs: R[] | null | undefined): R[] {
  return [...(runs ?? [])].sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt))
}

// Pending means an independent update unit has not returned its final result.
export function runTone(result: string): 'ok' | 'bad' | 'idle' {
  if (result === 'pass') return 'ok'
  if (result === 'fail' || result === 'error') return 'bad'
  // Pending, installing and cancelled runs are plain facts: muted, not colored.
  return 'idle'
}

export function exitLabel(code?: number | null): string {
  return code == null || code < 0 ? '' : `exit ${code}`
}

// Go prints 10 minutes as "10m0s". Show "10m".
export function timeoutLabel(t?: string | null): string {
  return String(t ?? '').replace(/(\d+h)0m0s$/, '$1').replace(/(\d+[hm])0s$/, '$1').replace(/(\d+h)0m$/, '$1')
}

// The events a new hook can use. main-moved is an old name for main-updated.
export const hookEvents = Object.keys(events).filter((e) => e !== 'main-moved')

// A new hook starts with the same defaults as `dyna git hooks add`.
// The editable form of a hook. A hook the engine stores has more fields; the form keeps the ones a person edits.
export interface HookFields {
  name: string
  event: string
  command: string
  cwd: string
  timeout: string
  onFail: string
  // A number input binds text, so the form may hold either.
  everyMinutes: number | string
}
export const newHook = (): HookFields => ({ name: '', event: 'before-land', command: '', cwd: 'worktree', timeout: '5m', onFail: 'alert', everyMinutes: 60 })

// hookForm is the editable copy of a stored hook.
export const hookForm = (h: Partial<Hook>): HookFields => ({ ...newHook(), ...h, timeout: timeoutLabel(h.timeout) || '5m', everyMinutes: h.everyMinutes || 60 })

// hookOf is the hook to save from the form. Only before-land can block,
// and only a schedule hook has an interval. The command stays exact:
// whitespace can change what a shell runs.
// What a save sends: a hook without the stored fields.
export type HookInput = Pick<Hook, 'name' | 'event' | 'command' | 'cwd' | 'timeout' | 'onFail'> & { everyMinutes?: number; disabled?: boolean }
export function hookOf(f: HookFields): HookInput {
  const h: HookInput = { name: f.name.trim(), event: f.event, command: f.command, cwd: f.cwd.trim(), timeout: f.timeout.trim(), onFail: f.event === 'before-land' ? f.onFail : 'alert' }
  if (f.event === 'schedule') h.everyMinutes = Number(f.everyMinutes)
  return h
}

// hookPatch lists only the fields the form changed, so an edit keeps the rest.
export function hookPatch(old: Hook, f: HookFields): Partial<HookInput> {
  const next = hookOf({ ...f, name: old.name })
  const patch: Partial<HookInput> = {}
  for (const k of ['command', 'event', 'cwd', 'onFail'] as const) if (next[k] !== old[k]) patch[k] = next[k]
  if (next.timeout !== timeoutLabel(old.timeout)) patch.timeout = next.timeout
  if (next.event === 'schedule' && next.everyMinutes !== old.everyMinutes) patch.everyMinutes = next.everyMinutes
  return patch
}
