import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parse } from 'svelte/compiler'
import { svelteFiles } from '../../../test/svelte-files.ts'

const lib = dirname(dirname(fileURLToPath(import.meta.url)))

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

// Matches `onkeydown` on any element, and `on:keydown` on a window element.
function isWindowKeyHandler(attr: unknown): boolean {
  return isRecord(attr) && (attr.name === 'onkeydown' || attr.type === 'OnDirective' && attr.name === 'keydown')
}

// Matches `aria-modal="true"`. Svelte stores the static value as a list of text nodes.
function isAriaModal(attr: unknown): boolean {
  if (!isRecord(attr) || attr.name !== 'aria-modal' || !Array.isArray(attr.value)) return false
  const [first] = attr.value
  return isRecord(first) && first.data === 'true'
}

test('all modal surfaces use the common keyboard and focus action', () => {
  let count = 0
  for (const path of svelteFiles(lib)) {
    const source = readFileSync(path, 'utf8')
    if (!source.includes('aria-modal="true"')) continue
    const ast = parse(source, { modern: true })
    assert.ok(ast.instance !== null)
    const imports = ast.instance.content.body.flatMap((node) =>
      node.type === 'ImportDeclaration' && typeof node.source.value === 'string' && node.source.value.endsWith('/dialog.ts') ? [node] : [])
    const aliases = imports.flatMap((node) => node.specifiers.flatMap((specifier) => {
      if (specifier.type !== 'ImportSpecifier') return []
      return specifier.imported.type === 'Identifier' && specifier.imported.name === 'dialog' ? [specifier.local.name] : []
    }))
    function walk(node: unknown): void {
      if (!isRecord(node)) return
      if (Array.isArray(node)) { node.forEach(walk); return }
      const attributes: unknown[] = Array.isArray(node.attributes) ? node.attributes : []
      if (node.type === 'SvelteWindow') {
        assert.ok(!attributes.some(isWindowKeyHandler), `${path}: keep modal shortcuts inside the dialog`)
      }
      if (attributes.some(isAriaModal)) {
        count++
        assert.ok(attributes.some((attr) => isRecord(attr) && attr.type === 'UseDirective' && typeof attr.name === 'string' && aliases.includes(attr.name)), path)
      }
      for (const [key, child] of Object.entries(node)) if (key !== 'attributes') walk(child)
    }
    walk(ast.fragment)
  }
  assert.ok(count >= 15, `Expected all modal components; found ${count}`)
})

test('Settings and Room settings share one modal shell', () => {
  for (const name of ['settings/Settings.svelte', 'rooms/RoomSettings.svelte']) {
    const source = readFileSync(join(lib, name), 'utf8')
    assert.match(source, /import SettingsShell from '\.\.\/ui\/SettingsShell\.svelte'/, name)
    assert.match(source, /<SettingsShell /, name)
    assert.doesNotMatch(source, /role="dialog"|class="shell"/, `${name} draws its own modal`)
  }
})

test('every settings pane that reports a modal clears it when it unmounts', () => {
  let count = 0
  for (const path of svelteFiles(lib)) {
    const source = readFileSync(path, 'utf8')
    if (!/onmodal\??\.?\(/.test(source)) continue
    count++
    assert.match(source, /onmodal\??\.?\(false\)/, `${path}: a pane that unmounts with a modal open leaves the settings modal busy`)
  }
  assert.ok(count >= 4, `Expected the settings panes; found ${count}`)
})
