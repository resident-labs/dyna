import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'esbuild'
import { compileSvelte, svelteEsbuildPlugin } from '../../../../test/svelte-esbuild.ts'
import { need } from '../../../../test/need.ts'
import { installDom, domNames } from '../../prs/prDetailHarness.ts'
import type { CallRequest } from '../../voice/callRequests.ts'

const dom = installDom()
const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'voice-host-'))
after(async () => {
  dom.window.close()
  await rm(dir, { recursive: true, force: true })
  for (const name of domNames) Reflect.deleteProperty(globalThis, name)
})
const shell = fileURLToPath(new URL('.', import.meta.url))
const fakeWidget = `<script>
  import { accepted, events, links } from 'fake-store'
  let { link = $bindable(null) } = $props()
  $effect(() => { if (link) links.push(link) })
  export function accept(req) { accepted.push(req) }
  export function decline(req) {}
  export function event(ev) { events.push(ev) }
</script>`
const wrapper = `<script>
  import VoiceHost from './VoiceHost.svelte'
  let roomId = $state('a')
  let calls = $state([])
  let host
  export function room(value) { roomId = value }
  export function current() { return calls }
  export function accept(req) { host.accept(req) }
  export function event(ev) { host.event(ev) }
  export function openLink(link) { host.openLink(link) }
</script>
<VoiceHost bind:this={host} bind:calls {roomId} narrow={false} rooms={[]} enabled={true} onopenfile={() => {}} />`
const built = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
  stdin: { contents: `export { default as Wrapper } from 'wrapper'; export { requests, accepted, events, loads, links } from 'fake-store'; export { mount, unmount, flushSync } from 'svelte'`, resolveDir: shell, loader: 'js' },
  plugins: [{
    name: 'voice-fakes',
    setup(builder) {
      builder.onResolve({ filter: /^wrapper$/ }, () => ({ path: 'wrapper', namespace: 'voice-fakes' }))
      builder.onResolve({ filter: /^fake-store$/ }, () => ({ path: 'store', namespace: 'voice-fakes' }))
      builder.onResolve({ filter: /voice\/VoiceWidget\.svelte$/ }, () => ({ path: 'widget', namespace: 'voice-fakes' }))
      builder.onResolve({ filter: /voice\/voiceCall\.ts$/ }, () => ({ path: 'store', namespace: 'voice-fakes' }))
      builder.onLoad({ filter: /^wrapper$/, namespace: 'voice-fakes' }, () => ({ contents: compileSvelte(wrapper, 'Wrapper.svelte'), loader: 'js', resolveDir: shell }))
      builder.onLoad({ filter: /^widget$/, namespace: 'voice-fakes' }, () => ({ contents: compileSvelte(fakeWidget, 'FakeWidget.svelte'), loader: 'js', resolveDir: shell }))
      builder.onLoad({ filter: /^store$/, namespace: 'voice-fakes' }, () => ({ contents: `import { writable } from 'svelte/store'; export const accepted = [], events = [], loads = [], links = []; export const requests = writable([]); export const callRequests = { subscribe: requests.subscribe, load: async room => { loads.push(room) } }`, loader: 'js', resolveDir: shell }))
    },
  }, svelteEsbuildPlugin()],
})
const file = join(dir, 'voice-host.mjs')
await writeFile(file, need(built.outputFiles[0]).contents)
interface Wrapper {
  room(value: string): void
  current(): CallRequest[]
  accept(request: CallRequest): void
  event(event: { kind: string }): void
  openLink(link: { room: string; call: string } | null): void
}
const web: {
  Wrapper: Parameters<typeof import('svelte').mount>[0]
  mount: (component: Parameters<typeof import('svelte').mount>[0], options: { target: Element }) => Wrapper
  unmount: (component: Wrapper) => Promise<void>
  flushSync: () => void
  requests: { set(requests: CallRequest[]): void }
  accepted: CallRequest[]
  events: { kind: string }[]
  loads: string[]
  links: { room: string; call: string }[]
} = await import(pathToFileURL(file).href)

test('room changes select current requests and events reach the widget once', async () => {
  const host = web.mount(web.Wrapper, { target: document.createElement('div') })
  web.flushSync()
  host.room('b')
  const request: CallRequest = { id: 'c1', room: 'b', reason: 'help', state: 'ringing', askedAt: '' }
  web.requests.set([{ ...request, room: 'a' }, request])
  host.event({ kind: 'call-requests' })
  host.openLink({ room: 'b', call: 'c1' })
  host.openLink(null)
  web.flushSync()
  assert.deepEqual(host.current(), [request])
  host.accept(need(host.current()[0]))
  assert.deepEqual(web.accepted, [request])
  assert.deepEqual(web.events, [{ kind: 'call-requests' }])
  assert.deepEqual(web.links, [{ room: 'b', call: 'c1' }])
  assert.deepEqual(web.loads, ['a', 'b'])
  await web.unmount(host)
})
