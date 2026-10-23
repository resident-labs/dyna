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
import type { EventPayload } from './events.gen.ts'
import type { Message } from '../api/room.ts'

// The registry test for the web side: every event in the generated registry
// has a renderer, every renderer has an event, and each one renders a real
// card from its typed payload on the feed surface and on the DM surface.
const rooms = fileURLToPath(new URL('.', import.meta.url))
const dom = installDom()
const dir = await mkdtemp(join(process.env.DYNA_SCRATCH ?? tmpdir(), 'dyna-event-renderers-'))
after(async () => {
  dom.window.close()
  await rm(dir, { recursive: true, force: true })
  for (const name of domNames) Reflect.deleteProperty(globalThis, name)
})

const built = await build({
  bundle: true, write: false, format: 'esm', platform: 'node', conditions: ['browser', 'production'], logLevel: 'silent',
  loader: { '.svg': 'dataurl' },
  stdin: {
    contents: `export { default as WorktreeCard } from './WorktreeCard.svelte';
      export { default as HookCard } from './HookCard.svelte';
      export { default as PRCard } from '../prs/PRCard.svelte';
      export { eventRenderers, eventComponents, eventKey, rendererFor, sealedEvent } from './eventRenderers.ts';
      export { hookEvent, prEvent, worktreeEvent } from './eventPayloads.ts';
      export { eventSpecs } from './events.gen.ts';
      export { mount, unmount, flushSync } from 'svelte';`,
    resolveDir: rooms,
    loader: 'js',
  },
  plugins: [svelteEsbuildPlugin()],
})
const app = join(dir, 'events.mjs')
await writeFile(app, need(built.outputFiles?.[0]).contents)
const web = await import(pathToFileURL(app).href)

// One typed fixture per registered event. A new event fails the test until it
// gets a fixture and a card.
const fixtures: Record<string, EventPayload> = {
  'worktree/kept': {
    owner: 'session-1',
    path: '/home/a/.dyna-worktrees/dyna-4708ae0b/composer-tidy',
    branch: 'composer-tidy',
    ahead: 1,
    dirty: ['notes.md'],
    reason: 'not-merged',
    detail: 'HEAD is not merged into origin/main; cleanup kept it',
  },
  'hook/started': {
    runId: 'ab12cd34ef56ab78cd90ef12ab34cd56', repo: '/home/a/Work/app', hook: 'self-update',
    event: 'main-updated', label: 'Update Dyna', command: 'dyna', state: 'running',
  },
  'hook/result': {
    runId: 'ab12cd34ef56ab78cd90ef12ab34cd56', repo: '/home/a/Work/app', hook: 'self-update',
    event: 'main-updated', label: 'Update Dyna', command: 'dyna', state: 'failed', durationMs: 4000,
    detail: 'exit 1',
  },
  ...Object.fromEntries(
    (['opened', 'ready', 'approved', 'changes', 'ci-failed', 'ci-paused', 'ci-passed', 'returned', 'merged', 'closed', 'comment', 'held-comment', 'review-assigned', 'update'] as const).map((type) => [
      `pr/${type}`,
      {
        type,
        label: 'PR #542',
        number: 542,
        location: 'https://github.com/acme/app/pull/542',
        changeId: 'github.com/acme/app#542',
        repo: 'acme/app',
        title: 'Fix the parser',
        head: 'a'.repeat(40),
        url: 'https://github.com/acme/app/pull/542',
        ciAttempt: type === 'ci-failed' ? 2 : undefined,
        ciMax: type === 'ci-failed' ? 3 : undefined,
        summary: 'unit, build',
        commenter: 'octocat',
        commentId: 'c1',
        verdict: 'approve',
        detail: `The body of the ${type} event.`,
      },
    ]),
  ),
}

function postFor(family: string, type: string, payload: EventPayload, text = ''): Message {
  return {
    id: 'msg_0123456789abcdef', ts: 1, roomId: 'room_1', from: 'dyna', to: ['room_1:owner'], wake: false,
    kind: family, notice: { kind: type }, payload, text,
  }
}

test('every registered event has a renderer and renders on feed and DM', () => {
  const specs = web.eventSpecs as readonly { family: string; type: string; renderer: string }[]
  assert.ok(specs.length > 0, 'the event registry is empty')
  for (const name of Object.keys(web.eventComponents)) {
    assert.ok(specs.some((spec) => spec.renderer === name), `renderer ${name} has no registered event`)
  }
  for (const spec of specs) {
    const key = web.eventKey(spec.family, spec.type)
    const Card = web.eventRenderers[key]
    assert.ok(Card, `${key} has no renderer`)
    const payload = fixtures[key]
    assert.ok(payload, `${key} has no test fixture`)
    const post = postFor(spec.family, spec.type, payload)
    assert.equal(web.rendererFor(post), Card, `${key} resolves to another renderer`)
    assert.ok(web.sealedEvent(post), `${key} is not a sealed family in the web registry`)
    for (const surface of ['feed', 'dm']) {
      const target = document.createElement('div')
      document.body.append(target)
      const mounted = web.mount(Card, { target, props: { post, notice: post.notice, surface } })
      web.flushSync()
      const text = (target.textContent ?? '').trim()
      assert.ok(text.length > 0, `${key} on ${surface} rendered nothing`)
      assert.doesNotMatch(text, /Unknown event/i, `${key} on ${surface} rendered the unknown row`)
      assert.ok(text.includes(fixtureTip(payload)), `${key} on ${surface} hid its payload: ${text}`)
      web.unmount(mounted)
      target.remove()
    }
    // A type with no renderer resolves to nothing, so the feed shows the
    // unknown event row instead of an empty gap.
    assert.equal(web.rendererFor({ ...post, notice: { ...post.notice, kind: 'unregistered' } }), undefined)
  }
})

// fixtureTip is a value every payload fixture shows, so a card that renders
// nothing meaningful fails.
function fixtureTip(payload: EventPayload): string {
  if ('hook' in payload) return payload.label || payload.hook
  if ('type' in payload) return payload.changeId ? `PR #${payload.number}` : payload.label
  return payload.detail || payload.reason
}

test('a hook card shows its state and opens the log of its own run', () => {
  const payload = fixtures['hook/result']
  assert.ok(payload && 'runId' in payload, 'the hook result fixture is missing')
  const post = postFor('hook', 'result', payload)
  const target = document.createElement('div')
  document.body.append(target)
  const asked: unknown[] = []
  const listen = (event: Event) => asked.push((event as CustomEvent<unknown>).detail)
  window.addEventListener('dyna:hook-log', listen)
  const mounted = web.mount(web.eventComponents.HookCard, { target, props: { post, notice: post.notice, surface: 'feed' } })
  web.flushSync()
  const text = (target.textContent ?? '').trim()
  assert.match(text, /Update Dyna/)
  assert.match(text, /failed/)
  assert.match(text, /4\.0 s/)
  const button = need(target.querySelector('button'), 'the View log button')
  button.click()
  assert.deepEqual(asked, [{ repo: payload.repo, hook: payload.hook, runId: payload.runId }])
  window.removeEventListener('dyna:hook-log', listen)
  web.unmount(mounted)
  target.remove()
  // A start shows as running and offers no duration, so the card never claims
  // a time it does not know.
  const running = postFor('hook', 'started', fixtures['hook/started'] ?? payload)
  const second = document.createElement('div')
  document.body.append(second)
  const mountedStart = web.mount(web.eventComponents.HookCard, { target: second, props: { post: running, notice: running.notice, surface: 'dm' } })
  web.flushSync()
  const startText = (second.textContent ?? '').trim()
  assert.match(startText, /running/)
  assert.doesNotMatch(startText, /\d+\.\d s/)
  web.unmount(mountedStart)
  second.remove()
})
