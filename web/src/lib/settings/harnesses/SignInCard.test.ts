import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { compileSvelteModule, svelteEsbuildPlugin } from '../../../../test/svelte-esbuild.ts'
import { installDom, domNames } from '../../prs/prDetailHarness.ts'
import { element } from '../../../../test/need.ts'

test('the card locks submitted choices, retries failed sends, and masks provider keys', async (t) => {
  const calls: string[] = []
  let finish: (sent: boolean) => void = () => {}
  const dom = installDom({
    signInPaste: (code: string) => {
      calls.push(code)
      return new Promise<boolean>((resolve) => { finish = resolve })
    },
  })
  Object.assign(globalThis, {HTMLMediaElement: dom.window.HTMLMediaElement, HTMLSelectElement: dom.window.HTMLSelectElement})
  const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'signin-card-test-'))
  t.after(async () => {
    dom.window.close()
    for (const name of [...domNames, 'HTMLMediaElement', 'HTMLSelectElement', 'signInPaste']) Reflect.deleteProperty(globalThis, name)
    await rm(dir, {recursive: true, force: true})
  })
  const source = `
    import SignInCard from './SignInCard.svelte'
    import { mount, unmount, flushSync } from 'svelte'
    export const props = $state({
      signIn: {phase: 'waiting', acceptsPaste: true, message: 'Choose a provider.', choices: [{id: 'anthropic', label: 'Anthropic'}]},
      busy: false, oncancel() {}, onpaste: code => globalThis.signInPaste(code)
    })
    export function start(target) { return mount(SignInCard, {target, props}) }
    export {unmount, flushSync}
  `
  const built = await build({
    bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
    stdin: {
      contents: compileSvelteModule(source, new URL('./SignInCardHarness.svelte.js', import.meta.url).pathname),
      resolveDir: new URL('.', import.meta.url).pathname,
    },
    plugins: [svelteEsbuildPlugin()],
  })
  const file = join(dir, 'card.mjs')
  await writeFile(file, built.outputFiles[0]?.contents ?? '')
  const m = await import(pathToFileURL(file).href)
  const app = m.start(document.getElementById('app'))
  const target = element(document, '#app')
  const submit = () => element(target, 'form').dispatchEvent(new Event('submit', {bubbles: true, cancelable: true}))
  const button = () => element<HTMLButtonElement>(target, 'button[type="submit"]')
  const settle = async () => { await Promise.resolve(); m.flushSync() }
  try {
    m.flushSync()
    submit()
    m.props.busy = true
    m.flushSync()
    finish(true)
    await settle()
    m.props.busy = false
    m.flushSync()
    assert.equal(button().disabled, true, 'busy ending must not unlock the old provider')
    submit()
    assert.deepEqual(calls, ['anthropic'])

    m.props.signIn = {...m.props.signIn, choices: [{id: 'anthropic', label: 'Anthropic'}]}
    m.flushSync()
    assert.equal(button().disabled, true, 'an identical poll is still the same step')

    m.props.signIn = {...m.props.signIn, message: 'Choose a login method.', choices: [{id: 'manual', label: 'Copy code'}]}
    m.flushSync()
    assert.equal(button().disabled, false, 'the next native step accepts its own choice')
    submit()
    finish(false)
    await settle()
    assert.equal(button().disabled, false, 'a failed send allows a retry')
    submit()
    finish(true)
    await settle()
    assert.equal(button().disabled, true)
    submit()
    assert.deepEqual(calls, ['anthropic', 'manual', 'manual'])

    m.props.signIn = {phase: 'waiting', acceptsPaste: true, secretInput: true, message: 'Paste your OpenCode Go API key.'}
    m.flushSync()
    const input = element<HTMLInputElement>(target, 'input')
    assert.equal(input.type, 'password', 'provider keys stay masked')
    input.value = 'fixture-api-key'
    input.dispatchEvent(new Event('input', {bubbles: true}))
    m.flushSync()
    submit()
    finish(true)
    await settle()
    assert.equal(calls.at(-1), 'fixture-api-key')
    assert.equal(input.value, '', 'sending clears the key from the card')
    assert.doesNotMatch(target.textContent ?? '', /fixture-api-key/)
  } finally {
    await m.unmount(app)
  }
})
