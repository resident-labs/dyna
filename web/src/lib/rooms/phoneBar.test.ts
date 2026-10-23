import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

// The top bar is CSS, not behaviour, so these tests read the markup and the
// rules a phone needs: no layout engine runs here. Live QA with a real browser
// reports the measurements for the pull request.
const app: string = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const lookMenu: string = readFileSync(new URL('./LookMenu.svelte', import.meta.url), 'utf8')
const toast: string = readFileSync(new URL('../voice/IncomingToast.svelte', import.meta.url), 'utf8')
const callButton: string = readFileSync(new URL('../voice/CallButton.svelte', import.meta.url), 'utf8')

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

test('a phone puts Members last in the bar and keeps the menu for pinned posts', () => {
  // The member list has its own button, so the overflow menu no longer
  // repeats Members or Issues, and the bar ends with Members after Search.
  const actions = header.indexOf('aria-label="Room actions"')
  assert.ok(actions > 0, 'the phone bar has one actions button')
  const narrow = header.lastIndexOf('{#if narrow}', actions)
  const desktop = header.indexOf('{:else}', actions)
  assert.ok(narrow > 0 && desktop > actions, 'the actions button is the phone branch of the bar')
  assert.equal(header.slice(narrow, desktop).includes('Show member list'), false, 'the phone branch holds the pin toggle only')
  const search = header.indexOf('aria-label="Search sessions and messages"')
  const members = header.indexOf("aria-label={showMembers ? 'Hide member list'")
  assert.ok(search > 0 && members > search, 'the Members button follows Search')
  assert.equal(header.slice(members).includes('class="icon"'), false, 'Members is the bar\'s last button')
  assert.match(app, /label: 'Pinned posts'/, 'the menu opens the pinned posts')
  assert.match(app, /label: 'Channel look'/, 'the menu opens the channel look')
  // The menu itself must not repeat a header button.
  const menuStart = app.indexOf('function channelMenuItems()')
  const menu = app.slice(menuStart, app.indexOf('\n  }', menuStart))
  assert.doesNotMatch(menu, /label: 'Members'/, 'Members is not repeated in the menu')
  assert.doesNotMatch(menu, /label: 'Issues'/, 'Issues is not repeated in the menu')
})

test('a phone title keeps a readable width and its buttons never shrink', () => {
  assert.match(rule(app, '.title, .crumb'), /min-width: 40px/, 'the title gives way before the bar controls')
  assert.match(rule(app, '.icon'), /flex: none/, 'bar buttons keep their 44 px touch target')
  assert.match(rule(callButton, '.callbtn'), /width: 44px/, 'the call button shrinks to its icon on a phone')
  assert.match(rule(callButton, '.call-label, .live-label'), /display: none/, 'only the call icon remains')
})

test('the room header shows its name and moves room tabs below the bar on phones', () => {
  assert.match(header, /<span class="name">\{roomLabel\(current \?\? room\)\}<\/span>/)
  assert.match(header, /\{#if !narrow\}<div class="dm-tabs"><Tabs tabs=\{roomTabs\}/)
  assert.match(app, /\{#if !workRow && narrow\}<Tabs tabs=\{roomTabs\}/)
  assert.doesNotMatch(header, /channelTrail|>general<|>Channels</)
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
