import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const declarations = parse(source).instance.content.body

function app(rootSeat) {
  const names = ['openMember', 'openSession', 'rootMember', 'sendToSession', 'showSession']
  const code = declarations.filter((n) => n.type === 'FunctionDeclaration' && names.includes(n.id.name)).map((n) => source.slice(n.start, n.end)).join('\n')
  const calls = []
  const context = vm.createContext({
    rootSeat, mergeSessionPage: (_s, v) => v, sessionReadingHistory: false, sessionError: 'x', roomId: 'room_a', room: { name: 'alpha' }, hierarchy: [{ owner: true, hue: 3 }], drill: null, session: null, sessionTab: 'chat', queue: [],
    detailRequest: 0, sessionVisit: 0, sessionTurn: 0, narrow: false, sessionView: null, loadSession: () => calls.push('load'), clearItemLink() {}, setTimeout() {},
    openView: (view) => calls.push('view:' + view),
    api: { send: async (...a) => { calls.push(['send', ...a]); return {} }, sendToSession: async (...a) => { calls.push(['sendToSession', ...a]); return {} }, profile: async () => ({}) },
  })
  vm.runInContext(code, context)
  return { context, calls }
}

test('an external seat holder still opens a working root DM, as a session loaded through RootSession', async () => {
  const { context, calls } = app({ external: true, harness: 'claude' })
  context.openSession({ rootSeat: true })
  assert.equal(context.drill.rootSeat, true)
  assert.equal(context.drill.address, 'alpha:root')
  assert.equal(context.drill.profile, 'claude (external) holds the seat')
  assert.equal(JSON.stringify(calls), JSON.stringify(['load']))
  await context.sendToSession('hello')
  assert.equal(JSON.stringify(calls.slice(1, 2)), JSON.stringify([['send', 'room_a', '@root hello', '', [], '']]), 'a root DM message goes to @root')
})

test('the root seat and the internal root both open as sessions, never as a separate view', async () => {
  const { context, calls } = app({ external: false })
  await context.openMember({ rootSeat: true })
  assert.equal(context.drill.rootSeat, true)
  context.openSession({ id: 'internal', role: 'root', name: 'root', address: 'alpha:root' })
  assert.equal(context.drill.id, 'internal')
  assert.equal(context.drill.rootSeat, undefined)
  await context.sendToSession('hi')
  assert.equal(calls.filter((c) => typeof c === 'string' && c.startsWith('view:')).length, 0)
  assert.equal(JSON.stringify(calls.find((c) => c[0] === 'sendToSession')), JSON.stringify(['sendToSession', 'internal', 'hi', [], '']))
})

test('there is no separate root view', () => {
  assert.doesNotMatch(source, /view === 'root'/)
  assert.doesNotMatch(source, /openView\('root'\)/)
  assert.doesNotMatch(source, /rootDMPost/)
})

test('a root with no session of its own does not claim to start a turn', () => {
  const session = readFileSync(new URL('../sessions/Session.svelte', import.meta.url), 'utf8')
  assert.match(session, /m\.role === 'root' && !m\.id\}\s*<span>Your message goes to @root/)
})

test('a root DM continues as the real session once root has one', () => {
  const { context, calls } = app({ external: false })
  context.openSession({ rootSeat: true })
  calls.length = 0
  context.showSession({ member: { role: 'root', name: 'root' }, turns: [] }) // still no session of its own
  assert.equal(context.drill.rootSeat, true)
  assert.equal(calls.length, 0)
  context.showSession({ member: { id: 'ses_root', role: 'root', name: 'root', address: 'alpha:root' }, turns: [{ n: 1 }] })
  assert.equal(context.drill.id, 'ses_root', 'the first message started root: queue, paging and events use its ID')
  assert.equal(context.drill.rootSeat, undefined)
  assert.equal(JSON.stringify(calls), JSON.stringify(['load']))
})

test('the root DM header has the voice call button, before Context; an ordinary agent DM does not', () => {
  const header = source.slice(source.indexOf('<UseIcons'), source.indexOf('>Context</button>'))
  assert.match(header, /\{#if isRootDm\}<CallButton \{room\} \/>\{\/if\}/)
  assert.match(source, /let isRootDm = \$derived\(!!drill && \(drill\.rootSeat \|\| drill\.role === 'root'\)\)/)
  assert.equal((source.match(/<CallButton /g) || []).length, 2, 'one in the room header, one in the root DM header')
})
