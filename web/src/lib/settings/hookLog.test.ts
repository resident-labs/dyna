import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { call, runFunctions } from '../../../test/svelte-source.ts'
import type { HookRun, HookRunLog } from '../api/settings.ts'
import type { ToastInput } from '../ui/toast.ts'

// Hooks belong to a repository, so Room settings is the only place that
// manages them, and a hook card's "View log" leads there.

const app = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const target = { repo: '/repo', hook: 'verify', runId: 'r1' }

interface OpenState extends Record<string, unknown> {
  roomSettingsAt?: { start: string; hookRun?: unknown }
  said: unknown[]
}

function openContext(room: unknown): OpenState {
  const state: OpenState = { navOpen: true, showSettings: true, showRoomSettings: false, roomSettingsKey: 0, room, said: [] }
  state.say = (input: ToastInput) => state.said.push(input)
  runFunctions(app, ['openHookLog'], state)
  return state
}

test('a hook card opens Room settings at Hooks, with its run in front', async () => {
  const state = openContext({ id: 'room-1' })
  await call(state, 'openHookLog', target)
  assert.equal(state.roomSettingsAt?.start, 'hooks')
  assert.equal(state.roomSettingsAt?.hookRun, target)
  assert.equal(state.showRoomSettings, true)
  assert.equal(state.showSettings, false)
  assert.equal(state.roomSettingsKey, 1)
  assert.equal(state.navOpen, false)
})

test('a hook card with no room says so instead of doing nothing', async () => {
  const state = openContext(null)
  await call(state, 'openHookLog', target)
  assert.equal(state.showRoomSettings, false)
  assert.equal(state.said.length, 1)
  const said = state.said[0] as ToastInput
  assert.equal(said.tone, 'info')
  assert.equal(said.subject, 'Open a room first')
  assert.equal(said.text, 'Hook runs belong to a room repository.')
})

test('the room Hooks section opens the run of the hook this repository holds', async () => {
  const pane = readFileSync(new URL('./HooksPane.svelte', import.meta.url), 'utf8')
  const runs: HookRun[] = [{ id: 'r1', repo: '/repo', name: 'verify', command: 'go test ./...', logPath: '', result: 'pass', startedAt: '2026-10-23T10:00:00Z' }]
  const asked: string[][] = []
  const log: HookRunLog = { tail: 'ok\n', exitCode: 0 }
  const api = {
    hookRuns: async (repo: string, name: string) => { asked.push(['runs', repo, name]); return runs },
    hookRunLog: async (repo: string, name: string, id: string): Promise<HookRunLog> => { asked.push(['log', repo, name, id]); return log },
  }
  const state: Record<string, unknown> = { api, repo: '/repo', hooks: [{ name: 'verify' }], runs: {}, logs: {}, open: '', openRun: '', error: '', newestFirst: <T>(x: T): T => x, errorText: String }
  runFunctions(pane, ['loadRuns', 'loadLog', 'showFocus'], state)

  // A card names the engine's repository root, and the room may store another
  // form of the same path. The loaded hook decides, not the path text.
  await call(state, 'showFocus', { repo: '/repo/', hook: 'verify', runId: 'r1' })
  assert.equal(state.open, 'verify')
  assert.equal(state.openRun, 'r1')
  assert.deepEqual(asked, [['runs', '/repo', 'verify'], ['log', '/repo', 'verify', 'r1']])
  const logs = state.logs as Record<string, HookRunLog>
  assert.equal(logs.r1?.tail, 'ok\n')

  // A hook this repository does not hold cannot show its run here.
  asked.length = 0
  await call(state, 'showFocus', { repo: '/elsewhere', hook: 'gone', runId: 'r9' })
  assert.match(String(state.error), /No hook named gone/)
  assert.deepEqual(asked, [])
})
