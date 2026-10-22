import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { parse } from 'svelte/compiler'

// Run the component's real send handler and teardown registration.
const source = readFileSync(new URL('./Composer.svelte', import.meta.url), 'utf8')
const declarations = parse(source).instance.content.body
const code = declarations.filter((node) =>
  node.type === 'FunctionDeclaration' && node.id.name === 'send' ||
  node.type === 'VariableDeclaration' && node.declarations.some((d) => d.id.name === 'active') ||
  node.type === 'ExpressionStatement' && node.expression.callee?.name === 'onDestroy',
).map((node) => source.slice(node.start, node.end)).join('\n')

function composer() {
  let resolve
  const pending = new Promise((r) => { resolve = r })
  const cleanup = []
  const calls = []
  const c = {
    command: null, forwarding: null, canSend: true, busy: false, error: '', note: '',
    $attachments: { atts: [] }, attachments: { destroy() {} }, draft: 'old channel draft', currentRoom: 'old-room',
    prepareText: () => pending,
    onDestroy: (fn) => cleanup.push(fn),
    onsend: async (text, atts) => { calls.push({ room: c.currentRoom, text, atts }); return {} },
    tick: async () => {}, setTimeout() {},
    field: { focus: () => calls.push('focus') },
  }
  vm.createContext(c)
  vm.runInContext(code, c)
  return { c, calls, resolve, destroy: () => cleanup.forEach((fn) => fn()) }
}

for (const navigation of ['room', 'channel']) {
  test(`pending preparation cannot send after ${navigation} navigation unmounts its composer`, async () => {
    const { c, calls, resolve, destroy } = composer()
    const sending = c.send()
    destroy()
    c.currentRoom = navigation === 'room' ? 'new-room' : 'old-room'
    c.draft = 'new channel draft'
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
