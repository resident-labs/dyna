import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'

// Exercise the actual reporter served by Go, including viewport resize events.
const go = readFileSync(new URL('../../../../internal/filesvc/files.go', import.meta.url), 'utf8')
const block = go.match(/const cardAutoHeight = ([\s\S]*?)\n\n/)?.[1]
assert.ok(block !== undefined, 'files.go has no cardAutoHeight block')
const script = [...block.matchAll(/`([^`]*)`/g)]
  .map((m) => {
    const text = m[1]
    assert.ok(text !== undefined)
    return text
  })
  .join('')
  .replace(/^<script>|<\/script>$/g, '')

interface Root {
  style: { overflowY?: string }
  clientHeight: number
  getBoundingClientRect: () => { height: number }
}

interface HeightMessage {
  type: 'dyna:height'
  height: number
}

// The card script posts these messages to its parent. The vm hands them over as unknown values.
function isHeightMessage(message: unknown): message is HeightMessage {
  if (typeof message !== 'object' || message === null) return false
  return 'type' in message && message.type === 'dyna:height' && 'height' in message && typeof message.height === 'number'
}

interface Reporter {
  root: Root
  messages: unknown[]
  resize: (viewport: number) => void
}

function reporter(height: number, viewport: number): Reporter {
  const events = new Map<string, () => void>()
  const messages: unknown[] = []
  const root: Root = { style: {}, clientHeight: viewport, getBoundingClientRect: () => ({ height }) }
  const c = {
    document: { documentElement: root, body: { scrollHeight: height } },
    innerHeight: viewport, window: {}, parent: { postMessage: (m: unknown) => messages.push(m) },
    addEventListener: (name: string, fn: () => void) => { events.set(name, fn) },
  }
  vm.runInNewContext(script, c)
  return {
    root, messages,
    resize(next: number) {
      c.innerHeight = root.clientHeight = next
      const onResize = events.get('resize')
      assert.ok(onResize !== undefined, 'the script did not listen for resize')
      onResize()
    },
  }
}

test('tall expanded cards and collapsed question previews retain native scrolling', () => {
  const r = reporter(2400, 640)
  assert.equal(r.root.style.overflowY, 'auto')
  const [first] = r.messages
  assert.ok(isHeightMessage(first))
  assert.equal(first.height, 2400)
  r.resize(320)
  assert.equal(r.root.style.overflowY, 'auto')
})

test('feed cards fit their reported height and scroll when the height cap clips them', () => {
  const fitting = reporter(2400, 2400)
  assert.equal(fitting.root.style.overflowY, 'hidden')
  fitting.resize(640)
  assert.equal(fitting.root.style.overflowY, 'auto')
  fitting.resize(2400)
  assert.equal(fitting.root.style.overflowY, 'hidden')
  assert.equal(fitting.messages.length, 1, 'resizing need not send an unchanged height')
  assert.equal(reporter(16000, 12000).root.style.overflowY, 'auto')
})
