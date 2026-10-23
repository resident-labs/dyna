import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { domNames, installDom, bundle, importBundle } from '../prs/prDetailHarness.ts'
import { element, elements, need, textOf } from '../../../test/need.ts'

const built = await bundle({ component: '../settings/ProfilesPane', api: `export const api = {
  configProfiles: () => Promise.resolve(structuredClone(globalThis.listed)),
}` })

test('profile filtering searches every field and preserves the complete uses grid and action context', async () => {
  const profile = (name: string, fields: object) => ({ name, uses: ['agents'], taste: 5, intelligence: 5, cost: 5, ...fields })
  const dom = installDom({ matchMedia: () => ({ matches: true }), listed: { default: 'alpha', profiles: [
    profile('alpha', { harness: 'codex', model: 'prime-a', models: ['prime-a', 'fallback-z'], description: 'Quick repair', default: true }),
    profile('beta', { harness: 'claude-code', model: 'sonnet', description: 'Design drafts' }),
    profile('gamma', { harness: 'opencode', model: 'archive-mini', description: 'Archive scans', disabled: true }),
  ] } })
  Object.assign(globalThis, { HTMLMediaElement: dom.window.HTMLMediaElement })
  const { dir, default: ProfilesPane, mount, flushSync, unmount } = await importBundle(built, 'ProfilesPane')
  let mounted: ReturnType<typeof mount> | undefined
  try {
    const target = need(document.getElementById('app'), 'app')
    mounted = mount(ProfilesPane, { target, props: {} })
    await Promise.resolve(); await Promise.resolve(); flushSync()
    const input = element<HTMLInputElement>(target, 'input[type="search"]')
    const names = () => elements(target, '.prof .name > span:first-child').map(textOf)
    const filter = (query: string) => {
      input.value = query
      input.dispatchEvent(new window.Event('input', { bubbles: true }))
      flushSync()
      assert.equal(elements(target, '.uses tbody tr').length, 3, 'uses include all profiles')
    }
    assert.notEqual(document.activeElement, input, 'filter does not take focus on mount')
    for (const [query, name] of [[' ALPHA ', 'alpha'], ['claude-code', 'beta'], ['prime-a', 'alpha'], ['FALLBACK-Z', 'alpha'], ['design drafts', 'beta'], ['Archive scans', 'gamma']]) {
      filter(need(query))
      assert.deepEqual(names(), [name])
      assert.match(textOf(element(target, '[role="status"]')), /1 of 3 profiles/)
    }
    filter('unmatched')
    assert.deepEqual(names(), [])
    assert.match(target.textContent, /No matching profiles/)
    element(target, '[aria-label="Clear profile filter"]').click()
    flushSync()
    assert.deepEqual(names(), ['alpha', 'beta', 'gamma'])
    assert.equal(document.activeElement, input)
    filter('alpha')
    element(target, '[aria-label="Actions for alpha"]').click()
    flushSync()
    need(elements(target, '[role="menuitem"]').find(el => textOf(el).trim() === 'Disable')).click()
    flushSync()
    const confirm = element(target, '[role="alertdialog"]')
    assert.match(confirm.textContent, /Disable alpha/)
    assert.match(confirm.textContent, /your default profile/)
    assert.doesNotMatch(confirm.textContent, /only enabled profile/)
  } finally {
    if (mounted) await unmount(mounted)
    dom.window.close()
    for (const name of [...domNames, 'listed', 'matchMedia', 'HTMLMediaElement']) Reflect.deleteProperty(globalThis, name)
    await rm(dir, { recursive: true, force: true })
  }
})
