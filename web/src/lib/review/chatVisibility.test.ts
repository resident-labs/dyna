import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { rm } from 'node:fs/promises'
import { build } from 'esbuild'
import { parse } from 'svelte/compiler'
import { compileSvelteModule } from '../../../test/svelte-esbuild.ts'
import { importBundle } from '../prs/prDetailHarness.ts'
import { moduleFunctions, sourceRange, stripTypes } from '../../../test/svelte-source.ts'
import { need } from '../../../test/need.ts'

const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const nodes = parse(source, { modern: true }).instance?.content.body ?? []
const codeOf = (node: object | undefined) => {
  const { start, end } = sourceRange(need(node))
  return stripTypes(source.slice(start, end))
}
// sessionVisible lives in lib/app/sessionView.svelte.ts now; the compiled
// snippet below gives it the page's own side.
const sessionViewSource = readFileSync(new URL('../app/sessionView.svelte.ts', import.meta.url), 'utf8')
const visible = [...moduleFunctions(sessionViewSource, ['sessionVisible']).values()][0]
if (!visible) throw new Error('sessionView has no sessionVisible')
const effect = codeOf(nodes.find((node) => node.type === 'ExpressionStatement' && node.expression.type === 'CallExpression' && node.expression.callee.type === 'Identifier' && node.expression.callee.name === '$effect' && codeOf(node).includes('const tab = sessionTab')))
// Run App's actual visibility effect with Svelte's dependency tracking.
const compiled = compileSvelteModule(`
  import { untrack } from 'svelte'; export { flushSync } from 'svelte';
  export function create() {
    let sessionTab = $state('chat'), pageVisible = $state(true), narrow = $state(true), reviewOpen = $state(true), drill = $state(null);
    const loads = [];
    const sessionHost = { pageOpen: () => pageVisible && !(narrow && reviewOpen), tab: () => sessionTab, say() {} };
    ${visible}
    const transcripts = { sessionVisible };
    function loadSession() { if (sessionVisible()) loads.push(drill.id) }
    const cleanup = $effect.root(() => { ${effect} });
    return { loads, cleanup, select() { drill = { id: 'idle-session' }; loadSession() }, close() { reviewOpen = false } };
  }
`, 'chat-visibility.svelte.js')
const built = await build({ stdin: { contents: compiled, resolveDir: new URL('../../', import.meta.url).pathname }, bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'] })

test('closing mobile Review loads the idle session selected while chat was hidden', async () => {
  const module = await importBundle(need(built.outputFiles?.[0]).contents, 'chat-visibility')
  const app = module.create()
  try {
    module.flushSync()
    app.select(); module.flushSync()
    assert.deepEqual(app.loads, [])
    app.close(); module.flushSync()
    assert.deepEqual(app.loads, ['idle-session'])
  } finally { app.cleanup(); await rm(module.dir, { recursive: true, force: true }) }
})
