import test from 'node:test'
import assert from 'node:assert/strict'
import { element, need } from '../../../test/need.ts'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from '../prs/prDetailHarness.ts'

const code = await bundle({
  component: '../rooms/Composer',
  format: `export const short = x => x; export const roomLabel = r => r.name; export const ago = () => ''; export const markdown = x => x; export const stamp = () => '';`,
  api: `export const api = {
    preview: () => Promise.resolve({mentions: [], wakes: []}),
    githubList: (_room, kind) => Promise.resolve({items: kind === 'pr' ? [{number: 123, title: 'Fix composer focus', state: 'OPEN', url: 'https://github.com/o/r/pull/123'}] : [{number: 123, title: 'Old composer issue', state: 'CLOSED', url: 'https://github.com/o/r/issues/123'}]}),
    issues: () => Promise.resolve([{id: 'msg_f05fdb28f01d83', number: 12, title: 'Keep the fixture running', state: 'open'}]),
  }`,
})
const settle = () => new Promise<void>((resolve) => setImmediate(resolve))

test('the attach picker accepts a typed number and sends a reference-only post', async (t) => {
  t.mock.timers.enable({apis: ['setTimeout']})
  const dom = installDom()
  Object.assign(globalThis, { HTMLMediaElement: dom.window.HTMLMediaElement, HTMLTextAreaElement: dom.window.HTMLTextAreaElement })
  const { default: Composer, mount, unmount, dir } = await importBundle(code, 'Composer')
  const sent: unknown[][] = []
  const target = need(document.getElementById('app'))
  const component = mount(Composer, { target, props: { room: { id: 'r', name: 'room' }, onsend: async (...args: unknown[]) => { sent.push(args); return {post: {message: {id: 'm'}}} } } })
  try {
    await settle()
    const attach = element<HTMLButtonElement>(target, '[aria-label="Attach a PR or issue"]')
    attach.click()
    await settle()
    const input = element<HTMLTextAreaElement>(target, 'textarea')
    assert.equal(input.value, '#')
    const rows = [...target.querySelectorAll<HTMLButtonElement>('[role="option"]')]
    assert.equal(rows.length, 3)
    for (const [index, title, label, state] of [
      [0, 'Fix composer focus', 'PR #123', 'open'],
      [1, 'Keep the fixture running', 'Issue D-12', 'open'],
      [2, 'Old composer issue', 'Issue #123', 'closed'],
    ] as const) {
      const row = need(rows[index])
      assert.equal(element(row, '.reference-title').textContent, title)
      const meta = element(row, '.reference-meta')
      assert.equal(element(meta, '.mono').textContent, label)
      assert.match(meta.textContent ?? '', new RegExp(state))
      assert.doesNotMatch(row.textContent ?? '', /msg_/)
    }
    assert.equal(element(need(rows[1]), '.room-tag').textContent, 'room')
    // The compact spelling finds the same room issue, and its chip keeps D-12.
    input.value = '#d12'
    input.setSelectionRange(4, 4)
    input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    await settle()
    const roomOption = element<HTMLButtonElement>(target, '[role="option"]')
    assert.match(roomOption.textContent ?? '', /Keep the fixture running.*Issue D-12/)
    roomOption.click()
    await settle()
    assert.match(element(target, '.references').textContent ?? '', /Issue D-12: Keep the fixture running/)
    element<HTMLButtonElement>(target, '[aria-label="Remove Issue D-12"]').click()
    await settle()
    input.value = '#123'
    input.setSelectionRange(4, 4)
    input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    await settle()
    const option = element<HTMLButtonElement>(target, '[role="option"]')
    assert.match(option.textContent ?? '', /Fix composer focus.*PR #123/)
    option.click()
    await settle()
    assert.equal(input.value, '')
    assert.match(element(target, '.references').textContent ?? '', /PR #123: Fix composer focus/)
    element<HTMLButtonElement>(target, '[aria-label="Send"]').click()
    await settle()
    assert.deepEqual(sent, [['', [], '', [{kind: 'pr', number: 123, title: 'Fix composer focus', state: 'OPEN', url: 'https://github.com/o/r/pull/123'}]]])
    assert.equal(target.querySelector('.references'), null)
  } finally {
    unmount(component)
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement', 'HTMLTextAreaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(dir, {recursive: true, force: true})
  }
})
