import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'
import { sourceRange, stripTypes } from '../../../test/svelte-source.ts'

// Run the component's real send handler and teardown registration.
const source = readFileSync(new URL('./Composer.svelte', import.meta.url), 'utf8')
const declarations = parse(source, { modern: true }).instance?.content.body ?? []
const code = declarations.filter((node) =>
  node.type === 'FunctionDeclaration' && node.id?.name === 'send' ||
  node.type === 'VariableDeclaration' && node.declarations.some((d) => d.id.type === 'Identifier' && d.id.name === 'active') ||
  node.type === 'ExpressionStatement' && node.expression.type === 'CallExpression' && node.expression.callee.type === 'Identifier' && node.expression.callee.name === 'onDestroy',
).map((node) => {
  const { start, end } = sourceRange(node)
  return source.slice(start, end)
}).join('\n')

interface Attachment {
  id: string
}

type Call = string | { room: string; text: string; atts: Attachment[] }

interface ComposerState {
  references: unknown[]
  command: null
  skillCommand: null
  forwarding: null
  canSend: boolean
  busy: boolean
  error: string
  note: string
  $attachments: { atts: Attachment[] }
  attachments: { destroy(): void }
  draft: string
  currentRoom: string
  prepareText: () => Promise<string>
  onDestroy: (fn: () => void) => void
  onpending: (pending: boolean) => void
  onsend: (text: string, atts: Attachment[]) => Promise<unknown>
  tick: () => Promise<void>
  setTimeout: () => void
  field: { focus: () => void }
}

interface ComposerApp extends ComposerState {
  send: () => Promise<void>
}

function composer() {
  let resolve: (text: string) => void = () => { throw new Error('no pending preparation') }
  const pending = new Promise<string>((r) => { resolve = r })
  const cleanup: (() => void)[] = []
  const calls: Call[] = []
  const c: ComposerState = {
    references: [], command: null, skillCommand: null, forwarding: null, canSend: true, busy: false, error: '', note: '',
    $attachments: { atts: [] }, attachments: { destroy() {} }, draft: 'old view draft', currentRoom: 'old-room',
    prepareText: () => pending,
    onDestroy: (fn: () => void) => cleanup.push(fn),
    onpending() {},
    onsend: async (text: string, atts: Attachment[]) => { calls.push({ room: c.currentRoom, text, atts }); return {} },
    tick: async () => {}, setTimeout() {},
    field: { focus: () => calls.push('focus') },
  }
  vm.createContext(c)
  vm.runInContext(stripTypes(code), c)
  const fns = vm.runInContext('({ send })', c) as Pick<ComposerApp, 'send'>
  return { c: Object.assign(c, fns), calls, resolve, destroy: () => cleanup.forEach((fn) => fn()) }
}

for (const navigation of ['room', 'view']) {
  test(`pending preparation cannot send after ${navigation} navigation unmounts its composer`, async () => {
    const { c, calls, resolve, destroy } = composer()
    const sending = c.send()
    destroy()
    c.currentRoom = navigation === 'room' ? 'new-room' : 'old-room'
    c.draft = 'new view draft'
    resolve('@child old channel draft')
    await sending
    assert.deepEqual(calls, [])
    assert.equal(c.busy, false)
  })
}

test('preparation still sends the resolved recipients while its composer remains open', async () => {
  const { c, calls, resolve } = composer()
  const sending = c.send()
  resolve('@child old channel draft')
  await sending
  assert.deepEqual(calls, [{ room: 'old-room', text: '@child old channel draft', atts: [] }, 'focus'])
  assert.equal(c.busy, false)
})

test('send passes attachments after the prepared text', async () => {
  const { c, calls, resolve } = composer()
  const atts = [{ id: 'attachment' }]
  c.$attachments.atts = atts
  const sending = c.send()
  resolve('@child with a file')
  await sending
  assert.deepEqual(calls, [{ room: 'old-room', text: '@child with a file', atts }, 'focus'])
})
