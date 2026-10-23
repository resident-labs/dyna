import { test } from 'node:test'
import assert from 'node:assert/strict'
import { byVisual, newestVersion } from './visuals.ts'

// Visual is the part of a checked attachment these rules read.
type Visual = { id: string; render?: string; version?: number; visual?: string }

test('a card shows the newest version of its visual', () => {
  const v1: Visual = { id: 'a1', render: 'html', version: 1 }
  const v2: Visual = { id: 'a2', render: 'html', visual: 'p1', version: 2 }
  const other: Visual = { id: 'b1', render: 'html', version: 1 }
  const byId = byVisual([
    { id: 'p1', attachment: v2, versions: [{ attachment: v2 }, { attachment: v1 }] },
    { id: 'p9', attachment: other, versions: [{ attachment: other }] },
  ])
  assert.equal(newestVersion(byId, 'p1', v1), v2, 'the first post shows v2')
  assert.equal(newestVersion(byId, 'p2', v2), v2, 'the update post shows v2')
  assert.equal(newestVersion(byId, 'p9', other), other)
  const unknown: Visual = { id: 'c1', render: 'html' }
  assert.equal(newestVersion(byId, 'p5', unknown), unknown, 'a card not loaded yet shows itself')
})

test('check status is the worst check, and the summary counts them', async () => {
  const { checkStatus, checksSummary, filterVisuals } = await import('./visuals.ts')
  const att = (...s: string[]) => ({ checks: s.map((status) => ({ status })) })
  assert.equal(checkStatus(att('pass', 'warn', 'pass')), 'warn')
  assert.equal(checkStatus(att('pass', 'fail', 'warn')), 'fail')
  assert.equal(checkStatus(att('skip', 'skip')), 'skip')
  assert.equal(checkStatus({}), 'none')
  assert.equal(checksSummary(att('pass', 'pass', 'pass', 'pass', 'warn')), '4 passed, 1 warning')
  assert.equal(checksSummary(att('warn', 'warn', 'fail')), '2 warnings, 1 failed')
  assert.equal(checksSummary(att('skip')), 'Checks skipped')
  assert.equal(checksSummary({}), 'No checks')
  const rows = [{ id: 'a', post: { owner: true }, attachment: att('pass') }, { id: 'b', post: {}, attachment: att('warn') }]
  assert.deepEqual(filterVisuals(rows, 'mine').map((v) => v.id), ['a'])
  assert.deepEqual(filterVisuals(rows, 'attention').map((v) => v.id), ['b'])
  assert.equal(filterVisuals(rows, 'all').length, 2)
})

test('an update post shows a chip for its own version; the first post shows the banner', async () => {
  const { placeholderHue, updates } = await import('./visuals.ts')
  const first: Visual = { id: 'a1', render: 'html', version: 1 }
  const update: Visual = { id: 'a2', render: 'html', visual: 'p1', version: 2 }
  assert.equal(updates(first), false)
  assert.equal(updates(update), true)
  assert.equal(placeholderHue('Merge queue'), placeholderHue('Merge queue'))
  assert.ok([...'abcdefgh'].map((c) => placeholderHue('Title ' + c)).every((h) => h >= 0 && h < 8))
  assert.ok(new Set([...'abcdefgh'].map((c) => placeholderHue('Title ' + c))).size > 1, 'titles spread over the name hues')
})

test('versions are picked by number, so reverted bytes keep their own version', async () => {
  const { versionOf } = await import('./visuals.ts')
  const a = { id: 'same-bytes', render: 'html' }
  const versions = [
    { post: { id: 'p3', text: 'Back to the first chart' }, attachment: { ...a, version: 3, checks: [{ name: 'renders', status: 'pass' }] } },
    { post: { id: 'p2' }, attachment: { id: 'other', version: 2 } },
    { post: { id: 'p1', text: 'First chart' }, attachment: { ...a, version: 1, checks: [] } },
  ]
  assert.equal(versionOf(versions, 1)?.post.id, 'p1', 'v1 keeps its own post and checks')
  assert.equal(versionOf(versions, 3)?.post.id, 'p3')
  assert.equal(versionOf(versions, 9)?.post.id, 'p3', 'an unknown version shows the newest')
})

// changesVisuals reads post events. A text-only post carries no attachments.
type VisualChange = Parameters<typeof import('./visuals.ts').changesVisuals>[0]
const change = (posts?: { text?: string; attachments?: { render?: string; visual?: string }[] }[]): VisualChange => ({ posts })

test('only posts with HTML attachments invalidate the visual list', async () => {
  const { changesVisuals } = await import('./visuals.ts')
  assert.equal(changesVisuals(change([{ text: 'hello' }])), false)
  assert.equal(changesVisuals({ posts: [{ attachments: [{ render: 'image' }] }] }), false)
  assert.equal(changesVisuals(change([{ attachments: [{ render: 'html', visual: 'p1' }] }])), true)
  assert.equal(changesVisuals({ posts: [{ attachments: [{ render: 'html' }] }] }), true)
  assert.equal(changesVisuals({}), true, 'an event without post data requires a refresh')
})
