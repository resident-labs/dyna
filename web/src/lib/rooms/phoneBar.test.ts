import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// The top bar is CSS, not behaviour, so these tests read the markup and the
// rules a phone needs: no layout engine runs here. Live QA with a real browser
// reports the measurements for the pull request. The bar itself lives in
// lib/app/shell/PageHeader.svelte; App.svelte holds the rest of the page.
const app: string = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const bar: string = readFileSync(new URL('../app/shell/PageHeader.svelte', import.meta.url), 'utf8')
const toast: string = readFileSync(new URL('../voice/IncomingToast.svelte', import.meta.url), 'utf8')
const callButton: string = readFileSync(new URL('../voice/CallButton.svelte', import.meta.url), 'utf8')

// The header markup, from the bar's opening tag to its closing tag.
const header = (() => {
  const start = bar.indexOf('<header class="bar"')
  assert.ok(start > 0, 'PageHeader.svelte has the top bar')
  return bar.slice(start, bar.indexOf('</header>', start))
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

test('the bar shows pinned posts as its own button and puts Members last', () => {
  // Pinned posts was the only entry left in the phone overflow menu, so the
  // menu is gone and the pin toggle sits in the bar on every width.
  assert.doesNotMatch(app, /Room actions|channelMenu/, 'the bar has no overflow menu')
  const pin = header.indexOf("aria-label={showPins ? 'Hide pinned posts'")
  assert.ok(pin > 0, 'the bar has the pinned posts button')
  assert.equal(header.slice(header.lastIndexOf('{/if}', pin), pin).includes('{:else}'), false, 'phones and desktops share the pin button')
  const search = header.indexOf('aria-label="Search sessions and messages"')
  const members = header.indexOf("aria-label={showMembers ? 'Hide member list'")
  assert.ok(search > 0 && members > search, 'the Members button follows Search')
  assert.match(header.slice(header.lastIndexOf('{#if', members), members), /room && !drill/, 'Members renders only inside a room, never on the home page')
  assert.equal(header.slice(members).includes('class="icon"'), false, 'Members is the bar\'s last button')
})

test('a phone title keeps a readable width and its buttons never shrink', () => {
  assert.match(rule(bar, '.title, .crumb'), /min-width: 40px/, 'the title gives way before the bar controls')
  assert.match(rule(bar, '.icon'), /flex: none/, 'bar buttons keep their 44 px touch target')
  assert.match(rule(callButton, '.callbtn'), /width: 44px/, 'the call button shrinks to its icon on a phone')
  assert.match(rule(callButton, '.call-label, .live-label'), /display: none/, 'only the call icon remains')
})

test('the room header shows its name, not a channel trail', () => {
  assert.match(header, /<span class="name">\{roomLabel\(current \?\? room\)\}<\/span>/)
  assert.doesNotMatch(header, /channelTrail|>general<|>Channels</)
})

test('the room header keeps no per-channel look control', () => {
  assert.doesNotMatch(header, /Customize this channel|LookMenu/, 'the bar has no channel look button')
  assert.doesNotMatch(app, /LookMenu|scopeOf|lookOpen/, 'nothing in the app scopes the look to one channel')
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
