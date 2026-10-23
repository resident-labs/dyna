import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, importBundle, installDom, domNames } from '../prs/prDetailHarness.ts'

const code = await bundle({ component: '../ui/Prompt' })

test('Prompt waits for IME confirmation before accepting normal submit keys and exact matches', async () => {
  for (const props of [{}, { multiline: true, ctrlKey: true }, { multiline: true, metaKey: true }, { match: 'DELETE' }]) {
    const dom = installDom()
    globalThis.HTMLMediaElement = dom.window.HTMLMediaElement
    const ui = await importBundle(code, '../ui/Prompt')
    const sent = []
    const component = ui.mount(ui.default, { target: document.getElementById('app'), props: {
      title: 'Edit', ...props, onsubmit: value => sent.push(value), oncancel() {},
    } })
    try {
      ui.flushSync()
      const field = document.querySelector('input, textarea')
      const action = document.querySelector('.go')
      const value = props.match || '日本語'
      const fill = text => { field.value = text; field.dispatchEvent(new Event('input', { bubbles: true })); ui.flushSync() }
      const key = options => field.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, ctrlKey: props.ctrlKey, metaKey: props.metaKey, ...options }))
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
