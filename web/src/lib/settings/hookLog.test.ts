import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import type { HookRun, HookRunLog } from '../api/settings.ts'

// Hooks belong to a repository, so Room settings is the only place that
// manages them, and a hook card's "View log" leads there.

test('a hook card opens Room settings at Hooks, never global Settings', async () => {
  const app = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
  const state: Record<string, unknown> & { roomSettingsAt?: { start: string; hookRun?: unknown } } = {
    navOpen: true, showSettings: true, showRoomSettings: false, roomSettingsKey: 0,
  }
  runFunctions(app, ['openHookLog'], state)
  const target = { repo: '/repo', hook: 'verify', runId: 'r1' }
  await call(state, 'openHookLog', target)
  assert.equal(state.roomSettingsAt?.start, 'hooks')
  assert.equal(state.roomSettingsAt?.hookRun, target)
  assert.equal(state.showRoomSettings, true)
  assert.equal(state.showSettings, false)
  assert.equal(state.roomSettingsKey, 1)
  assert.equal(state.navOpen, false)

  const settings = readFileSync(new URL('./Settings.svelte', import.meta.url), 'utf8')
  assert.doesNotMatch(settings, /HooksPane/)
  assert.doesNotMatch(settings, /id: 'hooks'/)
})

test('the room Hooks section opens the run a card asked for', async () => {
  const pane = readFileSync(new URL('./HooksPane.svelte', import.meta.url), 'utf8')
  const runs: HookRun[] = [{ id: 'r1', repo: '/repo', name: 'verify', command: 'go test ./...', logPath: '', result: 'pass', startedAt: '2026-10-23T10:00:00Z' }]
  const asked: string[][] = []
  const log: HookRunLog = { tail: 'ok\n', exitCode: 0 }
  const api = {
    hookRuns: async (repo: string, name: string) => { asked.push(['runs', repo, name]); return runs },
    hookRunLog: async (repo: string, name: string, id: string): Promise<HookRunLog> => { asked.push(['log', repo, name, id]); return log },
  }
  const state: Record<string, unknown> = { api, repo: '/repo', runs: {}, logs: {}, open: '', openRun: '', error: '', newestFirst: <T>(x: T): T => x, errorText: String }
  runFunctions(pane, ['loadRuns', 'loadLog', 'showFocus'], state)

  await call(state, 'showFocus', { repo: '/repo', hook: 'verify', runId: 'r1' })
  assert.equal(state.open, 'verify')
  assert.equal(state.openRun, 'r1')
  assert.deepEqual(asked, [['runs', '/repo', 'verify'], ['log', '/repo', 'verify', 'r1']])
  const logs = state.logs as Record<string, HookRunLog>
  assert.equal(logs.r1?.tail, 'ok\n')

  // A run of another repository cannot show under this room's hooks.
  asked.length = 0
  await call(state, 'showFocus', { repo: '/elsewhere', hook: 'verify', runId: 'r1' })
  assert.match(String(state.error), /\/elsewhere/)
  assert.deepEqual(asked, [])
})
