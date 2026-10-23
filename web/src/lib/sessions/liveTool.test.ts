import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { domNames, installDom, load } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'

const format = readFileSync(new URL('../ui/format.ts', import.meta.url), 'utf8')

// Mounts a component with the real format helpers and returns its text.
async function render(component: string, props: object) {
  const dom = installDom()
  Reflect.set(globalThis, 'HTMLMediaElement', dom.window.HTMLMediaElement)
  const { dir, default: C, mount, unmount, flushSync } = await load({ component, format })
  let app
  try {
    flushSync(() => { app = mount(C, { target: need(document.getElementById('app'), '#app'), props }) })
    return need(document.getElementById('app'), '#app').innerHTML
  } finally {
    if (app) unmount(app)
    dom.window.close()
    Reflect.deleteProperty(globalThis, 'HTMLMediaElement')
    for (const n of domNames) Reflect.deleteProperty(globalThis, n)
    await rm(dir, { recursive: true, force: true })
  }
}

test('the session view shows the current call from the live activity, not the first call of a grouped row', async (t) => {
  // The clock stands still, so a slow build cannot add seconds to "45s".
  const now = Date.now()
  t.mock.method(Date, 'now', () => now)
  const started = new Date(Date.now() - 600000).toISOString()
  // transcript grouping keeps the first read's target and time but marks the group running.
  const dm = { address: 'repo:root', threads: [], rows: [
    { kind: 'turn', key: 'turn:1', ts: started, turn: { n: 1, status: 'running', started } },
    { kind: 'timeline', key: 'timeline:1', ts: started, turn: { n: 1, started, status: 'running' }, steps: [{ kind: 'tool', key: 'tool:1', tool: 'read', target: 'old.go', ts: started }] },
  ] }
  const member = { name: 'root', hue: 1, state: 'live', address: 'repo:root', doing: { text: 'Read: new.go', since: now - 45000 } }
  const html = await render('../sessions/DmRows', { dm, member, room: { name: 'repo' }, members: [] })
  assert.match(html, /class="doing[^"]*long[^"]*"[^>]*>Read: new\.go · 45s</)
  assert.doesNotMatch(html, /old\.go · 10m/)
})

test('a live member row says what it is doing and for how long', async (t) => {
  const now = Date.now()
  t.mock.method(Date, 'now', () => now)
  const members = [
    { owner: true, name: 'repo', address: 'repo', children: 1, hue: 0 },
    { id: 's1', name: 'lead', address: 'repo:lead', profile: 'opus', state: 'live', status: 'running', turns: 3, depth: 0, hue: 2, doing: { text: 'Bash: Write a 10 GB test file', since: now - 45000 } },
  ]
  const html = await render('../rooms/Members', { members, onselect: () => {}, onclosed: () => {}, onmenu: () => {} })
  assert.match(html, /class="doing[^"]*long[^"]*"[^>]*>Bash: Write a 10 GB test file · 45s</)
})
