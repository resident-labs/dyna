import test from 'node:test'
import assert from 'node:assert/strict'
import type { Hook, HookRun } from '../api/settings.ts'
import { authorLabel, eventLabel, exitLabel, failLabel, hookEvents, hookForm, hookOf, hookPatch, listedHooks, newHook, newestFirst, runTone, timeoutLabel } from './hooks.ts'

// hook fills the fields the engine stores.
const hook = (fields: Pick<Hook, 'name' | 'event' | 'command' | 'cwd' | 'timeout' | 'onFail'>): Hook => ({ addedBy: 'owner', addedAt: '', scope: 'repository', ...fields })

test('events read as plain labels', () => {
  assert.equal(eventLabel('before-land'), 'Before land')
  assert.equal(eventLabel('main-updated'), 'Main updated')
  assert.equal(eventLabel('main-moved'), 'Main updated')
  assert.equal(eventLabel('pr-merged'), 'PR merged')
  assert.equal(eventLabel('schedule', 30), 'Schedule · every 30m')
  assert.equal(eventLabel('schedule', 720), 'Schedule · every 12h')
  assert.equal(eventLabel('later'), 'later')
})

test('on-fail and exit labels', () => {
  assert.equal(failLabel('block'), 'Block')
  assert.equal(failLabel('alert'), 'Alert')
  assert.equal(exitLabel(2), 'exit 2')
  assert.equal(exitLabel(0), 'exit 0')
  assert.equal(exitLabel(-1), '')
  assert.equal(exitLabel(undefined), '')
})

test('runs sort newest first and results map to tones', () => {
  const run = (id: string, startedAt: string): HookRun => ({ id, repo: 'r', name: 'n', command: 'c', result: 'pass', logPath: '', startedAt })
  const runs = [run('a', '2026-01-01T00:00:00Z'), run('b', '2026-01-02T00:00:00Z')]
  assert.deepEqual(newestFirst(runs).map((r) => r.id), ['b', 'a'])
  assert.deepEqual(newestFirst(null), [])
  assert.equal(runTone('pass'), 'ok')
  assert.equal(runTone('fail'), 'bad')
  assert.equal(runTone('error'), 'bad')
  assert.equal(runTone('cancelled'), 'idle')
})

test('timeouts drop empty units', () => {
  assert.equal(timeoutLabel('10m0s'), '10m')
  assert.equal(timeoutLabel('5m0s'), '5m')
  assert.equal(timeoutLabel('1h0m0s'), '1h')
  assert.equal(timeoutLabel('1h30m0s'), '1h30m')
  assert.equal(timeoutLabel('30s'), '30s')
  assert.equal(timeoutLabel('1m30s'), '1m30s')
})

test('a new hook uses the dyna git hooks add defaults and only valid fields', () => {
  assert.deepEqual(newHook(), { name: '', event: 'before-land', command: '', cwd: 'worktree', timeout: '5m', onFail: 'alert', everyMinutes: 60 })
  assert.equal(hookEvents.includes('main-moved'), false)
  const f = { ...newHook(), name: ' verify ', command: ' go test ./... ', onFail: 'block' }
  assert.deepEqual(hookOf(f), { name: 'verify', event: 'before-land', command: ' go test ./... ', cwd: 'worktree', timeout: '5m', onFail: 'block' })
  // Only before-land can block; only a schedule has an interval.
  assert.equal(hookOf({ ...f, event: 'after-land' }).onFail, 'alert')
  assert.equal(hookOf({ ...f, event: 'schedule', everyMinutes: '30' }).everyMinutes, 30)
})

test('an edit sends only the fields that changed', () => {
  const stored = hook({ name: 'verify', event: 'before-land', command: 'go test ./...', cwd: 'worktree', timeout: '10m0s', onFail: 'block' })
  const form = hookForm(stored)
  assert.equal(form.timeout, '10m')
  assert.deepEqual(hookPatch(stored, form), {})
  assert.deepEqual(hookPatch(stored, { ...form, command: 'make check', timeout: '5m' }), { command: 'make check', timeout: '5m' })
  // Moving off before-land turns a block into an alert.
  assert.deepEqual(hookPatch(stored, { ...form, event: 'after-land' }), { event: 'after-land', onFail: 'alert' })
  assert.deepEqual(hookPatch({ ...stored, event: 'schedule', onFail: 'alert', everyMinutes: 60 }, { ...form, event: 'schedule', onFail: 'alert', everyMinutes: 15 }), { everyMinutes: 15 })
})

test('the command is saved exactly; a timeout-only edit leaves it alone', () => {
  // printf "<%s>" foo\  prints <foo >; a trimmed copy would print <foo\>.
  const stored = hook({ name: 'echo', event: 'after-land', command: 'printf "<%s>" foo\\ ', cwd: 'worktree', timeout: '5m0s', onFail: 'alert' })
  const form = hookForm(stored)
  assert.deepEqual(hookPatch(stored, form), {})
  assert.deepEqual(hookPatch(stored, { ...form, timeout: '10m' }), { timeout: '10m' })
  assert.equal(hookOf({ ...form, name: 'echo' }).command, stored.command)
})

// A card never shows a raw session ID as the author.
test('the author of a hook reads in words', () => {
  assert.equal(authorLabel('owner'), 'you')
  assert.equal(authorLabel('engine'), 'Dyna')
  assert.equal(authorLabel('ses_20261023-1234abcd'), 'a session')
  assert.equal(authorLabel(''), 'someone')
})

test('the global screen hides the doctor built-in, never a custom hook of that name', () => {
  const hooks = [{ name: 'doctor', builtin: 'doctor' }, { name: 'doctor-mine' }, { name: 'doctor' }]
  assert.deepEqual(listedHooks(hooks, 'global'), [{ name: 'doctor-mine' }, { name: 'doctor' }])
  assert.equal(listedHooks(hooks, 'repository').length, 3)
})
