import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { svelteEsbuildPlugin } from '../../../test/svelte-esbuild.ts'
import { installDom, domNames } from '../prs/prDetailHarness.ts'
import { need } from '../../../test/need.ts'

const shell = fileURLToPath(new URL('./shell/', import.meta.url))
const dom = installDom()
const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-header-threads-'))
after(async () => {
  dom.window.close()
  await rm(dir, { recursive: true, force: true })
  for (const name of domNames) Reflect.deleteProperty(globalThis, name)
})

const built = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
  loader: { '.svg': 'dataurl' },
  stdin: {
    contents: `export { default as PageHeader } from './PageHeader.svelte';
      export { mount, unmount, flushSync } from 'svelte';`,
    resolveDir: shell,
    loader: 'js',
  },
  plugins: [svelteEsbuildPlugin()],
})
const app = join(dir, 'header.mjs')
await writeFile(app, need(built.outputFiles?.[0]).contents)
const web = await import(pathToFileURL(app).href)

function props(threadCount: number) {
  const noop = () => {}
  return {
    narrow: true, navOpen: false, showSide: false, roomId: 'room_1', room: { id: 'room_1', name: 'room_1' }, current: undefined,
    drill: { id: 'ses_root', name: 'root', address: 'room_1:root', hue: 2 }, session: null,
    machines: [], machineId: '', machineDetail: null, readOnlyDm: false, reviewOpen: false, people: [], live: [], pins: [],
    sessionTab: 'chat', sessionComputer: null, drillComputer: null, shells: 0, isRootDm: false, threadsOpen: false, threadCount,
    sessionTrail: [], view: 'general', openRun: '', runTrail: [], runDetail: null, workRow: undefined, workTabs: null,
    syncRemotes: [], syncRepo: undefined, syncNote: '', showPins: false, showMembers: false,
    onnavopen: noop, ontoggleside: noop, onback: noop, onsession: noop, onmember: noop, onthreads: noop, oncloserun: noop,
    onview: noop, onsyncrefresh: noop, onsyncexport: noop, onsearch: noop, ontogglepins: noop, onreview: noop, ontogglemembers: noop,
  }
}

test('the Threads badge is absent at zero and shows a positive count', () => {
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.PageHeader, { target, props: props(0) })
  web.flushSync()
  assert.equal(target.querySelector('.dm-threads b'), null)
  web.unmount(mounted)
  const counted = web.mount(web.PageHeader, { target, props: props(2) })
  web.flushSync()
  assert.equal(target.querySelector('.dm-threads b')?.textContent, '2')
  web.unmount(counted)
  target.remove()
})
