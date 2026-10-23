import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

const code = await bundle({ component: '../ui/Prompt' })

// The props each case hands the component; a case sets only what it changes.
interface PromptProps {
  multiline?: boolean
  ctrlKey?: boolean
  metaKey?: boolean
  match?: string
}
const cases: PromptProps[] = [{}, { multiline: true, ctrlKey: true }, { multiline: true, metaKey: true }, { match: 'DELETE' }]

test('Prompt waits for IME confirmation before accepting normal submit keys and exact matches', async () => {
  for (const props of cases) {
    const dom = installDom()
    Object.assign(globalThis, { HTMLMediaElement: dom.window.HTMLMediaElement })
    const ui = await importBundle(code, '../ui/Prompt')
    const sent: string[] = []
    const component = ui.mount(ui.default, { target: document.getElementById('app'), props: {
      title: 'Edit', ...props, onsubmit: (value: string) => sent.push(value), oncancel() {},
    } })
    try {
      ui.flushSync()
      const field = document.querySelector<HTMLInputElement | HTMLTextAreaElement>('input, textarea')
      const action = document.querySelector<HTMLButtonElement>('.go')
      assert.ok(field && action)
      const value = props.match || '日本語'
      const fill = (text: string) => { field.value = text; field.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync() }
      const key = (options: KeyboardEventInit) => field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ctrlKey: props.ctrlKey, metaKey: props.metaKey, ...options }))
      if (props.match) { fill('delete'); assert.equal(action.disabled, true) }
      fill(value)
      key({ isComposing: true })
      key({ keyCode: 229 })
      assert.deepEqual(sent, [])
      assert.equal(action.disabled, false)
      key({})
      ui.flushSync()
      assert.deepEqual(sent, [value])
      assert.equal(action.disabled, true)
    } finally {
      await ui.unmount(component)
      dom.window.close()
      for (const name of [...domNames, 'HTMLMediaElement']) Reflect.deleteProperty(globalThis, name)
      await rm(ui.dir, { recursive: true, force: true })
    }
  }
})
