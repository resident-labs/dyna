import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { compile } from 'svelte/compiler'
import { build } from 'esbuild'
import { need } from '../../../test/need.ts'

// Render the real shared PR card. Browser-only Markdown sanitizing and external
// navigation are mocked; the test checks full feedback binding and its label.
async function cardRenderer(name = 'PRCard') {
  const dir = await mkdtemp(join(tmpdir(), 'dyna-pr-ui-'))
  const result = await build({
    bundle: true, write: false,
    stdin: { contents: "export { default } from './" + name + ".svelte'; export { render } from 'svelte/server'", resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    plugins: [{ name: 'svelte-test', setup(builder) {
      builder.onLoad({ filter: /\.svelte$/ }, async ({ path }) => ({ contents: compile(readFileSync(path, 'utf8'), { filename: path, generate: 'server' }).js.code, loader: 'js', resolveDir: resolve(path, '..') }))
      builder.onLoad({ filter: /[/\\]format\.ts$/ }, () => ({ contents: "import { marked } from 'marked'; export const markdown = text => marked.parse(text || ''); export const ago = () => 'now';", loader: 'js', resolveDir: new URL('.', import.meta.url).pathname }))
      builder.onLoad({ filter: /[/\\]api\.js$/ }, () => ({ contents: 'export const api = {}; export const copyText = async () => {}; export const openExternal = () => { throw new Error("unexpected navigation") }', loader: 'js' }))
    } }],
  })
  // Bundle Svelte's matching server renderer together with the component so
  // they share the same SSR state instance.
  const componentPath = join(dir, 'card.mjs')
  await writeFile(componentPath, need(result.outputFiles?.[0]).contents)
  const module = await import(pathToFileURL(componentPath).href)
  return { dir, component: module.default, render: module.render }
}

test('loaded review shows full Markdown/file references and labels historical public feedback', async () => {
  const { dir, component, render } = await cardRenderer('Verdict')
  try {
    const summary = '## Findings\n\nFix `parser.go:42`.\n\n' + 'Review detail.\n\n'.repeat(150) + 'Tail: `tail.go:99`.'
    const pr = { number: 17, title: 'Parser', repo: 'owner/repo', url: 'https://github.com/owner/repo/pull/17', state: 'open', status: 'changes-requested', ci: 'passed', verdict: 'changes', summary, reviewPublishing: 'local', checks: [] }
    const local = render(component, { props: { pr } }).body
    assert.match(local, /Dyna only/)
    assert.match(local, /<h2[^>]*>Findings/)
    assert.match(local, /<code>parser\.go:42<\/code>/)
    assert.match(local, /<code>tail\.go:99<\/code>/)
    assert.equal((local.match(/Review detail\./g) || []).length, 150)
    assert.doesNotMatch(local, /Published on GitHub/)
    const historical = render(component, { props: { pr: { ...pr, reviewPublishing: '', reviewUrl: `${pr.url}#pullrequestreview-88` } } }).body
    assert.match(historical, /Published on GitHub/)
    assert.doesNotMatch(historical, /Dyna only/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})


test('shared PR card shows missing test report notes as plain text', async () => {
  const { dir, component, render } = await cardRenderer()
  try {
    const pr = { number: 17, title: 'Budget', repo: 'owner/repo', state: 'open', ci: 'passed', ciTestReportNote: 'No JUnit report. <script>unsafe</script>' }
    const html = render(component, { props: { pr } }).body
    assert.match(html, /No JUnit report\./)
    assert.match(html, /&lt;script(?:>|&gt;)unsafe&lt;\/script(?:>|&gt;)/)
    const empty = render(component, { props: { pr: { ...pr, ciTestReportNote: '' } } }).body
    assert.doesNotMatch(empty, /No JUnit report/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})


test('the real card requests review detail only after the owner opens it', async () => {
  const { dir, component, render } = await cardRenderer()
  try {
    const pr = { key: 'repo#17', roomId: 'fixture', state: 'open', ci: 'passed', verdict: 'changes', reviewState: 'changes', hasReviewSummary: true }
    const html = render(component, { props: { pr } }).body
    assert.match(html, /Read the review/)
    assert.doesNotMatch(html, /<h2[^>]*>Findings/)
  } finally { await rm(dir, { recursive: true, force: true }) }
})
