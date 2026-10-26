import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

const read = (p: string): string => readFileSync(new URL(p, import.meta.url), 'utf8')
// The Harnesses pane and the section components that now hold its markup.
const harnessSections = (): string => ['HarnessesPane', 'HarnessList', 'InstallControls', 'SettingsImportSection'].map((f) => read(`./harnesses/${f}.svelte`)).join('\n')
// The Skills and MCP panes, which now own the tool catalog.
const catalogPanes = (): string => ['skills/SkillsPane', 'skills/SkillForm', 'mcp/McpPane', 'mcp/McpForm'].map((f) => read(`./${f}.svelte`)).join('\n')

test('every settings loading line with an unknown layout is the shared muted Loading element', () => {
  const files = ['ProfileModal', 'ProfileWizard', 'harnesses/HarnessesPane', 'accounts/AccountsPane', 'accounts/LocalActivity']
  for (const f of files) {
    const text = read(`./${f}.svelte`)
    assert.match(text, /<Loading\b/, f)
    assert.doesNotMatch(text, /<p[^>]*>Loading [^<]*…<\/p>/, f)
  }
  const loading = read('./Loading.svelte')
  assert.match(loading, /color: var\(--faint\)/)
  assert.match(loading, /font-size: var\(--t-ui\)/)
  assert.doesNotMatch(loading, /sage|gold|coral/)
})

test('empty lists in the settings panes use EmptyState', () => {
  for (const f of ['ProfilesPane', 'DevicesPane', 'HooksPane', 'accounts/AccountsPane', 'accounts/ActivityReport']) {
    const text = read(`./${f}.svelte`)
    assert.match(text, /<EmptyState\b/, f)
    assert.doesNotMatch(text, /class="empty"/, f)
  }
  const panes = catalogPanes()
  assert.match(panes, /<EmptyState\b/)
  assert.doesNotMatch(panes, /class="empty"/)
})

test('Usage tabs use the shared filter-chip look', () => {
  const text = read('./accounts/AccountsPane.svelte')
  const css = text.slice(text.indexOf('<style>'))
  const tabs = css.split('\n').filter((l) => l.includes('.tabs'))
  assert.ok(tabs.some((l) => l.includes('var(--select)')))
  assert.ok(tabs.every((l) => !l.includes('var(--gold)') && !l.includes('::after')))
  assert.match(css, /\.tabs button \{[^}]*border-radius: 14px/)
})

test('the phone settings menu is two rows of four', () => {
  const css = read('../ui/SettingsShell.svelte')
  assert.match(css, /nav button \{ flex: 1 1 calc\(25% - 4px\)/)
  assert.doesNotMatch(css, /calc\(33\.333%/)
})

test('the settings browser fixture mocks every api call each pane makes', () => {
  const fixture = read('../../../tests/settings.ts')
  for (const f of ['ProfilesPane', 'accounts/AccountsPane', 'SettingsPane', 'DevicesPane', 'skills/SkillsPane', 'mcp/McpPane']) {
    const calls = new Set([...read(`./${f}.svelte`).matchAll(/\bapi\.(\w+)\(/g)].map((m) => m[1] ?? ''))
    for (const name of calls) assert.ok(fixture.includes(`api.${name} = `), `${f}: api.${name}`)
  }
  assert.ok(fixture.includes("query.has('empty')"))
})

test('every settings pane uses the shared TabHeader without its own h1', () => {
  for (const f of ['ProfilesPane', 'harnesses/HarnessesPane', 'accounts/AccountsPane', 'SettingsPane', 'DevicesPane', 'Settings']) {
    const text = read(`./${f}.svelte`)
    assert.match(text, /<TabHeader\b[^>]*\bflush\b/, f)
    assert.doesNotMatch(text, /<h1\b/, f)
    assert.doesNotMatch(text, /\.lede\b/, f)
  }
  assert.match(read('../rooms/TabHeader.svelte'), /\bflush\b/)
})

test('switching a settings pane fades and lifts the new pane in, with no exit wait', () => {
  const text = read('../ui/SettingsShell.svelte')
  assert.match(text, /\{#key pane\}/)
  assert.match(text, /in:fly=\{\{ y: 6, duration: motion\(BASE\) \}\}/)
  assert.doesNotMatch(text, /\b(out|transition):[a-z]/)
})

test('section labels share one global class', () => {
  assert.match(read('../ui/tokens.css'), /\.section-label \{[^}]*text-transform: uppercase/)
  for (const f of ['SettingsPane', 'DevicesPane', 'Settings']) {
    const text = read(`./${f}.svelte`)
    assert.match(text, /<h3 class="section-label"/, f)
    assert.doesNotMatch(text, /\n\s*(\.sec )?h3 \{/, f)
  }
  const harnesses = harnessSections()
  assert.match(harnesses, /<h3 class="section-label"/)
  assert.doesNotMatch(harnesses, /\n\s*(\.sec )?h3 \{/)
})

test('settings buttons use the one global .btn', () => {
  const css = read('../../app.css')
  assert.match(css, /\.btn \{[^}]*min-height: 30px/)
  for (const v of ['pri', 'quiet', 'danger', 'fill']) assert.match(css, new RegExp(`\\.btn\\.${v} \\{`), v)
  for (const f of ['ProfilesPane', 'DevicesPane', 'HooksPane', 'HookForm', 'harnesses/HarnessesPane', 'skills/SkillsPane', 'skills/SkillForm', 'mcp/McpPane', 'mcp/McpForm', 'accounts/AccountsPane']) {
    const text = read(`./${f}.svelte`)
    assert.match(text, /class="btn\b/, f)
    const style = text.slice(text.indexOf('<style>'))
    assert.doesNotMatch(style, /\n\s*(\.primary|\.ghost|\.button|\.revoke|\.go)\b/, f)
    assert.doesNotMatch(style, /\n\s*\.btn( |:)[^{]*\{[^}]*(background|border):/, f)
  }
  // A filled button keeps dark text, so pressing it must not turn the background dark.
  assert.match(css, /\.btn\.fill:active \{ background: var\(--iris-hover\)/)
  // Refresh keeps its 44 px touch height.
  assert.match(read('./accounts/AccountsPane.svelte'), /pointer: coarse\) \{ \.btn, /)
})

test('the catalog panes use the shared Switch, which keeps its size on phones', () => {
  const panes = catalogPanes()
  assert.match(panes, /import Switch from '..\/..\/ui\/Switch\.svelte'/)
  assert.doesNotMatch(panes, /class="sw"|\.sw\b/)
  const sw = read('../ui/Switch.svelte')
  assert.match(sw, /width: 42px; height: 24px/)
  assert.doesNotMatch(sw, /min-height/)
  assert.match(sw, /@media \(max-width: 760px\) \{[^}]*::before/)
  // The touch area is 50 px wide, so no tool grid column may be narrower.
  for (const [, px] of panes.matchAll(/repeat\(2, (\d+)px\)/g)) assert.ok(Number(px) >= 50, `grid column ${px}px`)
})

test('lists with a known layout load as skeleton rows', () => {
  for (const f of ['ProfilesPane', 'DevicesPane', 'HooksPane']) assert.match(read(`./${f}.svelte`), /<Skeleton\b/, f)
})

test('rows added to or removed from a list slide with the shared motion time', () => {
  for (const f of ['ProfilesPane', 'DevicesPane', 'HooksPane']) {
    assert.match(read(`./${f}.svelte`), /transition:slide=\{\{ duration: motion\(BASE\) \}\}/, f)
  }
})

test('a saved setting shows a quiet Saved that fades after 1.5 seconds', () => {
  const text = read('./SettingsPane.svelte')
  assert.match(text, /class="ok" transition:fade=\{\{ duration: motion\(FAST\) \}\}>Saved/)
  assert.match(text, /\}, 1500\)/)
})

test('removing one row asks inline, not in a dialog', () => {
  const inline = read('../ui/InlineConfirm.svelte')
  assert.match(inline, /btn danger/)
  assert.match(inline, /btn quiet/)
  for (const f of ['DevicesPane', 'HooksPane']) assert.match(read(`./${f}.svelte`), /<InlineConfirm\b/, f)
  assert.match(catalogPanes(), /<InlineConfirm\b/)
  assert.doesNotMatch(read('./HooksPane.svelte'), /<Confirm\b/)
  assert.doesNotMatch(harnessSections(), /requestRemoveTool/)
  // Harnesses has no local copy of the inline confirm: no coral button or Cancel of its own.
  const harnesses = harnessSections()
  assert.doesNotMatch(harnesses, /<button class="btn danger"/)
  assert.doesNotMatch(harnesses, /\(removing = ''\)\}>Cancel</)
  assert.match(harnesses, /ask="Uninstall Dyna’s copy\?"/)
  // Only the confirm button is coral; the row's own button stays quiet.
  for (const f of ['DevicesPane', 'HooksPane']) assert.doesNotMatch(read(`./${f}.svelte`), /<button class="btn danger"/, f)
  // Revoke all keeps the dialog because it explains more than one line.
  assert.match(read('./DevicesPane.svelte'), /<Confirm\b/)
})

test('the full-screen settings modals use the global .btn, with no local button styles', () => {
  for (const f of ['ProfileWizard', 'ProfileModal', 'ProfileEditor', 'SupportBrief', 'Pair', 'PairDevice']) {
    const text = read(`./${f}.svelte`)
    assert.match(text, /class="btn\b/, f)
    const css = text.slice(text.indexOf('<style>'))
    assert.doesNotMatch(css, /\.(primary|ghost|go|small)\b|^\s*button \{|footer button \{|\.row button \{/m, f)
  }
})
