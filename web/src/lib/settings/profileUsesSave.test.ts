import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { domNames, installDom, bundle, importBundle } from '../prs/prDetailHarness.ts'
import { element, need } from '../../../test/need.ts'

// The Profiles pane with an api the test controls: configProfiles reads
// globalThis.listed, and saveProfile waits for the test to settle it.
const API = `export const api = {
  configProfiles: () => Promise.resolve(structuredClone(globalThis.listed)),
  saveProfile: (edit) => new Promise((ok, fail) => globalThis.saves.push({ edit, ok, fail })),
}; export const openExternal = () => {}`
const built = await bundle({ component: '../settings/ProfilesPane', api: API })
const settle = () => new Promise((r) => setTimeout(r, 0))
const all = ['workflows', 'agents', 'services']
const prof = (name: string) => ({ name, harness: 'mock', model: '', models: [], envKeys: [], uses: [...all], taste: 5, intelligence: 5, cost: 5, support: { status: 'healthy', items: [] } })

// A save the pane has asked for; the test settles it.
interface Save {
  edit: { profile: { uses: string[] } }
  ok: (value: object) => void
  fail: (error: Error) => void
}
interface Listed {
  profiles: { uses: string[] }[]
}
const saves = (): Save[] => Reflect.get(globalThis, 'saves')
const listed = (): Listed => Reflect.get(globalThis, 'listed')
const save = (n: number): Save => need(saves()[n], `save ${n}`)

interface Pane {
  target: HTMLElement
  box: (use: string) => HTMLInputElement
  flushSync: () => void
}
async function pane(run: (pane: Pane) => Promise<void>) {
  const dom = installDom({ listed: { default: 'opus', harnesses: [], profiles: [prof('opus')] }, saves: [] })
  const { dir, default: ProfilesPane, mount, flushSync } = await importBundle(built, 'ProfilesPane')
  try {
    const target = need(document.getElementById('app'), 'app')
    mount(ProfilesPane, { target, props: {} })
    await settle(); flushSync()
    const box = (use: string) => element<HTMLInputElement>(target, `input[aria-label="opus: ${use}"]`)
    await run({ target, box, flushSync })
  } finally {
    dom.window.close()
    for (const n of [...domNames, 'listed', 'saves']) Reflect.deleteProperty(globalThis, n)
    await rm(dir, { recursive: true, force: true })
  }
}
const click = (el: HTMLInputElement, flushSync: () => void) => { el.checked = !el.checked; el.dispatchEvent(new window.Event('change', { bubbles: true })); flushSync() }

test('a row stays busy until its save and reload finish, so two changes both persist', async () => {
  await pane(async ({ box, flushSync }) => {
    click(box('workflows'), flushSync)
    assert.equal(saves().length, 1)
    assert.deepEqual(save(0).edit.profile.uses, ['agents', 'services'])
    assert.equal(box('agents').disabled, true, 'the row is locked while it saves')
    need(listed().profiles[0]).uses = ['agents', 'services']
    save(0).ok({})
    await settle(); await settle(); flushSync()
    assert.equal(box('agents').disabled, false)
    click(box('agents'), flushSync)
    assert.deepEqual(save(1).edit.profile.uses, ['services'], 'the second save keeps the first change')
  })
})

test('a failed save stays on screen after the list reloads', async () => {
  await pane(async ({ target, box, flushSync }) => {
    click(box('workflows'), flushSync)
    save(0).fail(new Error('profile is locked'))
    await settle(); await settle(); flushSync()
    assert.match(target.textContent ?? '', /profile is locked/)
    assert.equal(box('workflows').checked, true, 'the list shows the saved state')
  })
})
