import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// The top bar is CSS, not behaviour, so these tests read the markup and the
// rules a phone needs: no layout engine runs here. Live QA with a real browser
// reports the measurements for the pull request.
const app: string = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const lookMenu: string = readFileSync(new URL('./LookMenu.svelte', import.meta.url), 'utf8')
const toast: string = readFileSync(new URL('../voice/IncomingToast.svelte', import.meta.url), 'utf8')

// The header markup, from the bar's opening tag to its closing tag.
const header = (() => {
  const start = app.indexOf('<header class="bar"')
  assert.ok(start > 0, 'App.svelte has the top bar')
  return app.slice(start, app.indexOf('</header>', start))
})()

// The declarations of the last rule that matches selector, which is the phone
// rule where a file has one. CSS here keeps every declaration block balanced,
// so the first closing brace ends the rule.
const rule = (source: string, selector: string): string => {
  const pattern = new RegExp(`\\n\\s*${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} \\{`, 'g')
  let at = -1
  for (const m of source.matchAll(pattern)) at = m.index
  assert.ok(at >= 0, `a rule sets ${selector}`)
  const open = source.indexOf('{', at)
  return source.slice(open + 1, source.indexOf('}', open))
}

test('a phone keeps the page title readable and moves the panel toggles into one menu', () => {
  // Four 44 px buttons took the whole bar, so the title shrank to 3 letters.
  const actions = header.indexOf('aria-label="Channel actions"')
  assert.ok(actions > 0, 'the phone bar has one actions button')
  const narrow = header.lastIndexOf('{#if narrow}', actions)
  const desktop = header.indexOf('{:else}', actions)
  assert.ok(narrow > 0 && desktop > actions, 'the actions button is the phone branch of the bar')
  for (const label of ['Show pinned posts', 'Show issues', 'Show member list']) {
    assert.equal(header.slice(narrow, desktop).includes(label), false, `${label} leaves the phone bar`)
    assert.ok(desktop < header.indexOf(label), `${label} stays in the desktop bar`)
  }
  assert.match(app, /label: 'Members'/, 'the menu opens the member list')
  assert.match(app, /label: 'Pinned posts'/, 'the menu opens the pinned posts')
  assert.match(app, /label: 'Channel look'/, 'the menu opens the channel look')
})

test('a phone title keeps a readable width and its buttons never shrink', () => {
  assert.match(rule(app, '.title, .crumb'), /min-width: 96px/, 'the title never shrinks below 96 px')
  assert.match(rule(app, '.icon'), /flex: none/, 'bar buttons keep their 44 px touch target')
})

test('a nested channel shows one crumb on a phone instead of overflowing the bar', () => {
  // Every crumb holds 96 px, so a trail of three plus the bar's buttons left the
  // controls outside a 390 px viewport.
  assert.match(rule(app, '.trail .crumb:not(.current), .trail .slash'), /display: none/, 'older crumbs and their slashes leave the phone bar')
  assert.match(header, /class="crumb" class:current=\{!channelTrail\.length\} title=\{trailTitle\}/, 'the root crumb is current on the root channel')
  assert.match(header, /class:current=\{i === channelTrail\.length - 1\}/, 'the last trail crumb is the current channel')
  assert.match(app, /let trailTitle = \$derived\(\['general', \.\.\.channelTrail\.map\(\(a\) => a\.name\)\]\.join\(' \/ '\)\)/, 'the hidden trail stays in the tooltip')
})

test('the channel look popover stays inside a 390 px viewport', () => {
  const pop = rule(lookMenu, '.pop')
  assert.match(pop, /position: fixed/, 'the popover is pinned to the viewport, not to a button near the edge')
  assert.match(pop, /left: 8px/, 'the popover keeps 8 px on the left')
  assert.match(pop, /right: 8px/, 'the popover keeps 8 px on the right')
  assert.doesNotMatch(pop, /right: 0/, 'the popover no longer hangs off the button')
  assert.match(pop, /max-height:/, 'a long popover scrolls instead of leaving the screen')
})

test('the incoming call card docks above the composer instead of over the top bar', () => {
  const narrow = rule(toast, '.narrow')
  assert.match(narrow, /top: auto/, 'the card no longer starts at the top of the screen')
  assert.match(narrow, /bottom: calc\(96px \+ env\(safe-area-inset-bottom\)\)/, 'the card sits above the composer')
  assert.match(rule(toast, '.narrow .who p'), /line-clamp: 2/, 'the reason stays short so the card stays small')
})

test('a work view gives the tab labels the bar and keeps the repo path in a tooltip', () => {
  const work = header.slice(header.indexOf('{:else if workRow}'), header.indexOf('{:else}', header.indexOf('{:else if workRow}')))
  assert.equal(work.includes('class="topic mono"'), false, 'the code page no longer prints the repo path in the bar')
  assert.match(work, /<span class="title" title=\{room\.repo\}>/, 'the repo path is the code title tooltip')
})
