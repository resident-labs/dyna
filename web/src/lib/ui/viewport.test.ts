import test from 'node:test'
import assert from 'node:assert/strict'
import { trackViewportHeight, type ViewportHost } from './viewport.ts'

// A window whose listeners the test can see, so it can resize it the way a
// browser does.
function fakeWindow(inner: number, visual: number | undefined) {
  const values = new Map<string, string>()
  const listeners: { target: string; name: string; run: () => void }[] = []
  const removal: { target: string; name: string; run: () => void }[] = []
  const events = (target: string) => ({
    addEventListener: (name: string, run: () => void) => { listeners.push({ target, name, run }) },
    removeEventListener: (name: string, run: () => void) => { removal.push({ target, name, run }) },
  })
  const viewport = visual === undefined ? null : { height: visual, ...events('visualViewport') }
  const host = {
    innerHeight: inner,
    visualViewport: viewport,
    document: { documentElement: { style: { setProperty: (name: string, value: string) => { values.set(name, value) } } } },
    ...events('window'),
  }
  return {
    host: host satisfies ViewportHost,
    height: () => values.get('--app-height'),
    listeners,
    removal,
    setVisual(next: number) { if (viewport) viewport.height = next },
    setInner(next: number) { host.innerHeight = next },
  }
}

test('the shell height follows the visible viewport, not the layout viewport', () => {
  const win = fakeWindow(844, 528)
  trackViewportHeight(win.host)
  assert.equal(win.height(), '528px', 'a browser bar or a zoom leaves the layout viewport taller than the screen')
})

test('a window without a visual viewport falls back to its own height', () => {
  const win = fakeWindow(700, undefined)
  trackViewportHeight(win.host)
  assert.equal(win.height(), '700px')
})

test('a resize or a rotation updates the height, and the cleanup stops listening', () => {
  const win = fakeWindow(844, 844)
  const stop = trackViewportHeight(win.host)
  assert.equal(win.height(), '844px')
  win.setVisual(600)
  for (const listener of win.listeners.filter((l) => l.name === 'resize')) listener.run()
  assert.equal(win.height(), '600px', 'an open keyboard shrinks the shell')
  win.setInner(900)
  for (const listener of win.listeners.filter((l) => l.name === 'orientationchange')) listener.run()
  assert.equal(win.height(), '600px', 'the visible viewport still wins after a rotation')
  win.setVisual(900)
  for (const listener of win.listeners.filter((l) => l.name === 'orientationchange')) listener.run()
  assert.equal(win.height(), '900px')
  stop()
  for (const listener of win.listeners) {
    assert.ok(win.removal.some((r) => r.name === listener.name && r.run === listener.run), `cleanup removed ${listener.target} ${listener.name}`)
  }
})
