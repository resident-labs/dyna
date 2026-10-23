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
    issues: () => Promise.resolve([]),
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
    input.value = '#123'
    input.setSelectionRange(4, 4)
    input.dispatchEvent(new dom.window.Event('input', { bubbles: true }))
    await settle()
    const option = element<HTMLButtonElement>(target, '[role="option"]')
    assert.match(option.textContent ?? '', /PR #123.*Fix composer focus/)
    option.dispatchEvent(new dom.window.MouseEvent('mousedown', { bubbles: true }))
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
