import test from 'node:test'
import assert from 'node:assert/strict'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { renderedBody } from '../../../test/svelte-render.ts'
import { call } from '../../../test/svelte-source.ts'
import type { RunResultView } from '../api/workflow.ts'

test('worker detail distinguishes a confirmed empty result from absent or populated output', async () => {
  const built = await build({
    bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['node'], logLevel: 'silent',
    stdin: { contents: "export { default } from './WorkerDetail.svelte'; export { render } from 'svelte/server'", resolveDir: new URL('.', import.meta.url).pathname, loader: 'js' },
    plugins: [svelteEsbuildPlugin('server')],
  })
  const output = built.outputFiles?.[0]
  assert.ok(output)
  const module = await import('data:text/javascript;base64,' + Buffer.from(output.contents).toString('base64'))
  const body = (value?: RunResultView) => renderedBody(call(module, 'render', module.default, { props: { value } }))
  const empty = 'No output has been recorded for this worker.'
  assert.doesNotMatch(body(), /No output/)
  assert.ok(body({ result: '', bytes: 0, journal: [] }).includes(empty))
  const journal = [{ ts: 1, kind: 'progress', message: 'First entry' }]
  for (const value of [
    { result: 'Result text', bytes: 11 },
    { result: '', bytes: 0, journal },
    { result: 'Result text', bytes: 11, journal },
  ]) assert.ok(!body(value).includes(empty))
})
