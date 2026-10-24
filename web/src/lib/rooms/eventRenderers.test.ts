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
import type { EventPayload, HookEvent } from './events.gen.ts'
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
      export { default as MergeCard } from './MergeCard.svelte';
      export { default as PRCard } from '../prs/PRCard.svelte';
      export { eventRenderers, eventComponents, eventKey, rendererFor, sealedEvent } from './eventRenderers.ts';
      export { hookEvent, mergeDecision, mergeEvent, prEvent, reviewEvent, worktreeEvent } from './eventPayloads.ts';
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
  'machine/low-disk': {
    name: 'hermeslab',
    freeBytes: 3 * 1024 ** 3,
    floorBytes: 20 * 1024 ** 3,
    summary: '3.0 GiB free, needs 20 GiB',
    atMs: 1792774800000,
  },
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
    (['filed', 'updated', 'closed'] as const).map((type) => [
      `issue/${type}`,
      {
        type,
        issueId: type === 'filed' ? undefined : 'msg_0123456789abcdef',
        number: 3,
        title: 'Two retry loops in the agent runner',
        files: [{ path: 'internal/agents/run.go', line: 42 }],
        symbols: ['Runner.retry'],
        cost: 'Two retry paths are easy to drift apart.',
        direction: 'Keep one retry loop with an injected clock.',
        state: type === 'closed' ? 'closed' : type === 'updated' ? 'goal' : 'open',
        filer: 'dyna:librarian',
        actor: type === 'filed' ? undefined : 'dyna:root',
        reason: type === 'closed' ? 'Fixed by PR #542' : undefined,
        closedBy: type === 'closed' ? { pr: 542, commit: '24c91eb' } : undefined,
        detail: `The note of the ${type} issue.`,
      },
    ]),
  ),
  ...Object.fromEntries(
    (['added', 'edited', 'finished', 'dropped'] as const).map((type) => [
      `goal/${type}`,
      {
        type,
        goalId: 'g318',
        title: 'Ship typed issue events',
        actor: 'dyna:notice-issues',
        state: type === 'finished' ? 'done' : type === 'dropped' ? 'dropped' : 'active',
        summary: type === 'finished' ? 'Merged as PR #542' : undefined,
      },
    ]),
  ),
  'checkin/summary': {
    parent: 'dyna:notice-contract',
    windowStart: 1792227600000,
    counts: { running: 1, queued: 0, idle: 2 },
    children: [
      { name: 'notice-merge', state: 'idle', age: '42m', goal: 'g317/i12: merge events', pr: 'open PR #269: ready' },
      { name: 'notice-pr', state: 'running', age: '3m', goal: 'no open goal or item', pr: 'no open PRs' },
      { name: 'reviewer-pool', state: 'idle', age: '9m', goal: 'g319: review queue', pr: 'no open PRs' },
    ],
    detail: 'notice-merge · idle for 42m · g317/i12: merge events · open PR #269: ready',
  },
  'ci-health/slow-test': {
    run: '#1241',
    count: 1,
    crossings: [
      {
        test: 'dyna-agent/internal/room/TestPostEvent', package: 'internal/room', features: ['rooms'],
        durationMs: 2470, run: '#1241', otherMs: 1200, otherRun: '#1238', limitMs: 1000,
      },
    ],
    detail: 'dyna-agent/internal/room/TestPostEvent · internal/room · rooms · 2.47 s at #1241, 1.2 s at #1238 · limit 1 s',
  },
  'rule/changed': {
    ruleId: 'r4',
    scope: 'room',
    actor: 'dyna:owner',
    verb: 'edited',
    text: 'A subagent commits to its parent branch.',
    sessions: ['dyna:notice-one', 'dyna:notice-two'],
  },
  'review/result': {
    kind: 'changes',
    changeId: 'github.com/acme/app#542',
    number: 542,
    label: 'PR #542',
    location: 'https://github.com/acme/app/pull/542',
    head: 'a'.repeat(40),
    summary: 'Two findings need a fix.',
    findings: [
      { id: 'f1', severity: 'blocking', file: 'internal/room/events.go', line: 42, why: 'the field is required but empty', fix: 'make it optional' },
      { id: 'f2', severity: 'nit', file: 'web/src/lib/rooms/ReviewResultCard.svelte', line: 0, why: 'name is unclear', fix: 'rename the prop' },
    ],
    blocking: 1,
    shouldFix: 0,
    nits: 1,
    detail: 'Two findings need a fix.',
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
  ...Object.fromEntries(
    (['queued', 'trial', 'checks', 'landed', 'failed', 'returned', 'conflict', 'rolled-back'] as const).map((type) => [
      `merge/${type}`,
      {
        type,
        trialId: 'dyna/merge-trial/7',
        repo: 'acme/app',
        base: 'main',
        prs: [{ number: 242, title: 'Tidy the composer', url: 'https://github.com/acme/app/pull/242' }],
        checks: { done: 4, total: 9, failed: type === 'failed' ? ['unit'] : undefined },
        commit: type === 'landed' ? 'b'.repeat(40) : undefined,
        reason: type === 'failed' || type === 'returned' || type === 'conflict' ? 'trial CI failed' : undefined,
        label: 'PR #242',
      },
    ]),
  ),
  'merge/decision': { note: 'Keep the queue paused until the flake is fixed' },
  'merge/fix-requested': {
    type: 'fix-requested',
    trialId: 'github.com/acme/app#242',
    repo: 'acme/app',
    base: 'main',
    prs: [{ number: 242, title: 'Tidy the composer', url: 'https://github.com/acme/app/pull/242' }],
    head: 'c'.repeat(40),
    actor: 'the room owner (ari)',
    label: 'PR #242',
  },
  ...Object.fromEntries(
    (['joined', 'declined', 'withdrawn'] as const).map((type) => [`call/${type}`, { type, call: 'c1' }]),
  ),
  ...Object.fromEntries(
    (['renamed', 'promoted', 'adopted', 'account', 'instructions', 'seat', 'spawned', 'transfer-request'] as const).map((type) => [
      `session/${type}`,
      {
        type,
        session: 'dyna:notice-session',
        name: type === 'renamed' || type === 'spawned' ? 'notice-session' : undefined,
        oldName: type === 'renamed' ? 'notice-events' : undefined,
        from: type === 'account' ? 'claude' : type === 'promoted' ? 'dyna:notice-contract' : type === 'adopted' ? 'dyna:root' : undefined,
        to: type === 'account' ? 'claude-2' : type === 'promoted' ? 'top level' : type === 'adopted' ? 'dyna:notice-contract' : undefined,
        reason: type === 'account' ? 'usage-limit' : undefined,
        profile: type === 'spawned' ? 'deepseek' : type === 'transfer-request' ? 'sol' : undefined,
        dir: type === 'spawned' ? '/home/a/Work/app' : undefined,
        seat: type === 'seat' ? 'Internal root took back the seat' : undefined,
        transfer: type === 'transfer-request' ? 'ses_20261023-120000-00000001' : undefined,
        sections: type === 'instructions' ? [{ name: 'Room rules', action: 'items', added: ['Rule 4'], removed: ['Rule 2'] }] : undefined,
        detail: type === 'transfer-request'
          ? 'Prepare a handoff for the replacement model. Submit it with `dyna subagent transfer handoff ses_20261023-120000-00000000 --token ses_20261023-120000-00000001`.'
          : `The ${type} note body.`,
      },
    ]),
  ),
  'session/instruction-batch': {
    sections: [{ name: 'Subagent profiles', action: 'items', added: ['- writer | model: model-2'] }],
    sessions: ['dyna:notice-one', 'dyna:notice-two'],
    notes: ['msg_0000000000000001', 'msg_0000000000000002'],
  },
  'alert/doctor': {
    source: 'doctor', reportId: '7', trigger: 'hook main-updated',
    summary: 'machine is overloaded',
    findings: [
      { severity: 'critical', title: 'three builds run at once', cause: 'load 7.9 per 8 cores, PSI some avg10 22%' },
      { severity: 'warning', title: 'the test shard holds the lock too long', cause: '3.1 s over the 1 s limit' },
    ],
    nextCommand: 'dyna doctor report 7',
    detail: 'machine is overloaded\n1. three builds run at once. Next: `dyna doctor report 7`',
  },
  'alert/engine': {
    source: 'engine',
    summary: 'The engine has been read-only for more than 2 minutes because migration failed.',
    remedy: 'systemctl --user restart dyna-engine.service',
    detail: 'only the engine creates and migrates dyna.db',
  },
  'service/failed': {
    service: 'librarian', session: 'dyna:librarian', turn: 4,
    error: 'turn failed: harness exited with status 1', profile: 'deepseek', model: 'deepseek-flash',
  },
  'service/helper-outcome': {
    reason: 'no-artifact', session: 'dyna:helper-check', turn: 2,
  },
  'engine/root-flush': { minutes: 15 },
  'question/withdrawn': { questionId: 'q1', post: 'msg_0123456789abcdef', by: 'dyna:notice-session', reason: 'session closed' },
  'shell/result': {
    shellId: 'sh_20261023-120000-0000000a',
    label: 'go test · the room package',
    state: 'failed',
    exitCode: 1,
    durationMs: 41_200,
    output: ['--- FAIL: TestRoomPost (0.02s)', 'FAIL dyna-agent/internal/room 0.03s'],
    parent: 'dyna:notice-contract',
    detail: 'Full output: dyna shell tail sh_20261023-120000-0000000a',
  },
  'run/result': {
    runId: 'wf_20261023-000001-aaaaaaaa',
    name: 'review-prs',
    state: 'completed',
    summary: 'Reviewed 3 pull requests, filed 2 findings.',
    steps: 3,
    agents: 3,
    durationMs: 184_000,
    parent: 'dyna:notice-contract',
    detail: 'Full output: dyna runs show wf_20261023-000001-aaaaaaaa',
  },
  'turn/completed': {
    session: 'dyna:notice-merge',
    sessionId: 'ses_20261023-120000-00000000',
    turn: 4,
    state: 'completed',
    summary: 'PR 3b is open and ready for review. CI is green.',
    detail: 'Full output: dyna subagent output dyna:notice-merge',
  },
  'turn/failed': {
    session: 'dyna:notice-merge',
    sessionId: 'ses_20261023-120000-00000000',
    turn: 5,
    state: 'failed',
    error: 'the provider refused the request',
    detail: 'Inspect: dyna subagent output ses_20261023-120000-00000000 --turn 5 --transcript',
  },
  'turn/stopped': {
    session: 'dyna:notice-merge',
    sessionId: 'ses_20261023-120000-00000000',
    turn: 6,
    state: 'stopped',
    summary: 'Stopped after the owner asked for a wrap-up.',
  },
}

// hookFixture reads a hook payload by name, so a test reads the fields of its
// own event rather than the union of every payload.
function hookFixture(type: 'started' | 'result'): HookEvent {
  const payload = fixtures[`hook/${type}`]
  assert.ok(payload && 'hook' in payload, `the hook/${type} fixture is missing`)
  return payload
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
  if ('kind' in payload && 'changeId' in payload) return payload.label
  if ('note' in payload) return payload.note ?? ''
  if ('hook' in payload) return payload.label || payload.hook
  if ('trialId' in payload) return '#242'
  if ('changeId' in payload) return `PR #${payload.number}`
  if ('filer' in payload) return `D-${payload.number}`
  if ('goalId' in payload) return payload.goalId
  if ('crossings' in payload) return payload.run
  if ('children' in payload) return payload.children[0]?.name ?? payload.parent
  if ('ruleId' in payload) return payload.sessions?.length ? `reached ${payload.sessions.length} sessions` : payload.ruleId
  if ('floorBytes' in payload) return payload.name
  if ('source' in payload) return payload.reportId ?? payload.remedy ?? payload.summary ?? payload.source
  if ('service' in payload) return payload.error
  if ('minutes' in payload) return String(payload.minutes)
  if ('questionId' in payload) return payload.questionId
  if ('shellId' in payload) return payload.label
  if ('runId' in payload) return payload.name
  if ('call' in payload) return payload.type === 'withdrawn' ? 'Call request' : 'the call'
  if ('sessions' in payload) return `reached ${payload.sessions.length} sessions`
  if ('session' in payload) {
    // A turn end names its session and carries no session change of its own.
    if (!('type' in payload)) return payload.session
    if (payload.type === 'account') return payload.to ?? payload.session
    if (payload.type === 'renamed' || payload.type === 'spawned') return payload.name ?? payload.session
    if (payload.type === 'seat') return payload.seat ?? payload.session
    if (payload.type === 'transfer-request') return payload.profile ?? payload.session
    return payload.session
  }
  return payload.detail || payload.reason
}

test('a call row says how the owner answered, without its request ID', () => {
  for (const [type, words] of [['joined', /Owner joined the call/], ['declined', /Owner declined the call/], ['withdrawn', /Call request withdrawn/]] as const) {
    const payload = fixtures[`call/${type}`]
    assert.ok(payload, `the call/${type} fixture is missing`)
    for (const surface of ['feed', 'dm'] as const) {
      const post = postFor('call', type, payload, `The owner ${type} the call c1.`)
      const target = document.createElement('div')
      document.body.append(target)
      const mounted = web.mount(web.eventComponents.CallCard, { target, props: { post, notice: post.notice, surface } })
      web.flushSync()
      const text = (target.textContent ?? '').trim()
      assert.match(text, words, `call/${type} on ${surface}: ${text}`)
      // The owner reads what happened, not the request's ID. The ID stays in
      // the row's tooltip, and in the agent text the DM keeps behind Details.
      const row = need(target.querySelector('.call'), 'the call row')
      assert.doesNotMatch(row.textContent ?? '', /\bc1\b/, `call/${type} on ${surface} shows the request ID`)
      assert.equal(row.getAttribute('title'), 'Call request c1')
      web.unmount(mounted)
      target.remove()
    }
  }
})

test('a transfer request names its target and keeps the token in details', () => {
  const payload = fixtures['session/transfer-request']
  assert.ok(payload && 'transfer' in payload, 'the transfer-request fixture is missing')
  const token = /ses_20261023-120000-00000001/
  const feed = postFor('session', 'transfer-request', payload, '[DYNA MODEL TRANSFER] Prepare a handoff for transfer ses_20261023-120000-00000001:')
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.eventComponents.SessionNoteCard, { target, props: { post: feed, notice: feed.notice, surface: 'feed' } })
  web.flushSync()
  const summary = need(target.querySelector('.sum'), 'the feed summary').textContent ?? ''
  assert.match(summary, /dyna:notice-session: Handoff requested for sol/)
  assert.doesNotMatch(summary, token)
  assert.equal(target.querySelector('.id'), null)
  // The feed keeps the request identity with the agent instructions, not in
  // the owner-facing summary.
  const instructions = need(target.querySelector('details'), 'the instructions fold').textContent ?? ''
  assert.match(instructions, /dyna subagent transfer handoff/)
  assert.match(instructions, token)
  web.unmount(mounted)
  target.remove()

  const dmText = '[DYNA MODEL TRANSFER] Prepare a handoff for transfer ses_20261023-120000-00000001:\n\nPrepare a handoff. Submit it with `dyna subagent transfer handoff ses_20261023-120000-00000000 --token ses_20261023-120000-00000001`.'
  const dm = postFor('session', 'transfer-request', payload, dmText)
  const dmTarget = document.createElement('div')
  document.body.append(dmTarget)
  const mountedDm = web.mount(web.eventComponents.SessionNoteCard, { target: dmTarget, props: { post: dm, notice: dm.notice, surface: 'dm' } })
  web.flushSync()
  const dmSummary = need(dmTarget.querySelector('.sum'), 'the DM summary').textContent ?? ''
  // The DM already belongs to the session, so its summary states the target
  // and handoff status without repeating the session or exposing the token.
  assert.match(dmSummary, /Handoff requested for sol/)
  assert.doesNotMatch(dmSummary, /dyna:notice-session:/)
  assert.doesNotMatch(dmSummary, token)
  assert.doesNotMatch(dmTarget.textContent ?? '', /dyna subagent transfer handoff/)
  need(dmTarget.querySelector('button'), 'the Details button').click()
  web.flushSync()
  assert.match(dmTarget.textContent ?? '', /dyna subagent transfer handoff/)
  assert.match(dmTarget.textContent ?? '', token)
  web.unmount(mountedDm)
  dmTarget.remove()
})

test('a merge-fix request names who asked and shows no trial progress', () => {
  const payload = fixtures['merge/fix-requested']
  assert.ok(payload && 'actor' in payload, 'the fix-requested fixture is missing')
  const post = postFor('merge', 'fix-requested', payload, 'Merge fix requested by the room owner (ari) for PR #242.')
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.eventComponents.MergeCard, { target, props: { post, notice: post.notice, surface: 'feed' } })
  web.flushSync()
  const text = (target.textContent ?? '').trim()
  assert.match(text, /Merge fix requested for #242 into main/)
  assert.match(text, /Requested by the room owner \(ari\)/)
  // A request has no trial, so the card shows no progress bar for one.
  assert.equal(target.querySelector('.steps'), null, 'a merge-fix request drew a trial progress bar')
  web.unmount(mounted)
  target.remove()
})

test('a post stored before the typed events still draws its family card', () => {
  // Old history names no event type. The family's card reads the stored text,
  // so a sealed family never falls to the unknown-event row on old posts.
  for (const [family, renderer] of [
    ['goal', 'GoalPost'], ['issue', 'IssueCard'], ['checkin', 'CheckinCard'],
    ['ci-health', 'SlowTestCard'], ['rule', 'RuleCard'], ['pr', 'PRCard'], ['hook', 'HookCard'],
  ] as const) {
    const legacy = { id: 'msg_0123456789abcdef', ts: 1, kind: family, text: 'stored text' }
    assert.ok(web.sealedEvent(legacy), `${family} is not a sealed family`)
    assert.equal(web.rendererFor(legacy), web.eventComponents[renderer], `${family} lost its legacy card`)
  }
  // A type the registry does not know is still a visible gap, not an old post.
  assert.equal(web.rendererFor({ id: 'msg_0123456789abcdef', ts: 1, kind: 'goal', notice: { kind: 'unregistered' } }), undefined)
})

test('an event family from a newer engine is sealed instead of shown as raw text', () => {
  const post = { id: 'msg_0123456789abcdef', ts: 1, kind: 'future-family', notice: { kind: 'created' }, text: 'raw event text' }
  assert.equal(web.sealedEvent(post), true)
  assert.equal(web.rendererFor(post), undefined)
  assert.equal(web.sealedEvent({ ...post, kind: 'message', notice: undefined }), false)
})

test('a hook card shows its state and opens the log of its own run', () => {
  const payload = hookFixture('result')
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
  // The glyph is a drawn icon, never an emoji.
  assert.doesNotMatch(text, /\u2699/)
  assert.ok(target.querySelector('svg'), 'the card lost its icon')
  const button = need(target.querySelector('button'), 'the View log button')
  button.click()
  assert.deepEqual(asked, [{ repo: payload.repo, hook: payload.hook, runId: payload.runId }])
  window.removeEventListener('dyna:hook-log', listen)
  web.unmount(mounted)
  target.remove()
  // A start shows as running and offers no duration, so the card never claims
  // a time it does not know.
  const running = postFor('hook', 'started', hookFixture('started'))
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

test('a folded hook card shows a run that is still going and opens its log', () => {
  const payload = hookFixture('result')
  assert.ok(payload && 'runId' in payload, 'the hook result fixture is missing')
  const post = postFor('hook', 'result', payload)
  const target = document.createElement('div')
  document.body.append(target)
  const asked: unknown[] = []
  const listen = (event: Event) => asked.push((event as CustomEvent<unknown>).detail)
  window.addEventListener('dyna:hook-log', listen)
  // The newest post passed, yet an older run of the hook has no result: the
  // card says running and its log is the running run's log.
  const mounted = web.mount(web.eventComponents.HookCard, {
    target,
    props: { post, notice: post.notice, surface: 'feed', hookFold: { count: 3, lastTs: 0, active: { runId: 'run-still-going', ts: 0 } } },
  })
  web.flushSync()
  assert.match((target.textContent ?? '').trim(), /running/)
  need(target.querySelector('button'), 'the View log button').click()
  assert.deepEqual(asked, [{ repo: payload.repo, hook: payload.hook, runId: 'run-still-going' }])
  window.removeEventListener('dyna:hook-log', listen)
  web.unmount(mounted)
  target.remove()
})

test('a folded hook card keeps its count and last time outside the name', () => {
  const payload = hookFixture('result')
  assert.ok(payload && 'runId' in payload, 'the hook result fixture is missing')
  const post = postFor('hook', 'result', payload)
  const target = document.createElement('div')
  document.body.append(target)
  const passed = { ...payload, state: 'passed' as const }
  const mounted = web.mount(web.eventComponents.HookCard, {
    target,
    props: { post: { ...post, payload: passed }, notice: post.notice, surface: 'feed', hookFold: { count: 12, lastTs: Date.parse('2026-10-22T14:00:00Z') } },
  })
  web.flushSync()
  const text = (target.textContent ?? '').trim()
  assert.match(text, /12 times/)
  assert.match(text, /passed/)
  // The card shows the count and the last time in an element of its own, never
  // inside the name, so the narrow stylesheet can give that line its own row
  // and the name keeps its whole width. The stylesheet decides where the line
  // sits; this test holds the markup it depends on.
  const fact = need(target.querySelector('.fact'), 'the fold fact')
  assert.match(fact.textContent ?? '', /12 times · last/)
  assert.equal(fact.closest('.title'), null, 'the fold fact is inside the name line')
  assert.equal(target.querySelector('.title .name')?.textContent, 'Update Dyna', 'the name is cut short')
  web.unmount(mounted)
  target.remove()
})

test('an alert card lists its typed findings and copies the next command', async () => {
  const payload = fixtures['alert/doctor']
  assert.ok(payload && 'nextCommand' in payload, 'the doctor alert fixture is missing')
  const post = postFor('alert', 'doctor', payload)
  const target = document.createElement('div')
  document.body.append(target)
  const copied: string[] = []
  Object.defineProperty(globalThis, 'navigator', {
    configurable: true,
    value: { clipboard: { writeText: async (text: string) => { copied.push(text) } } },
  })
  const mounted = web.mount(web.eventComponents.AlertCard, { target, props: { post, notice: post.notice, surface: 'feed' } })
  web.flushSync()
  const text = (target.textContent ?? '').trim()
  // Both causes are rows of their own, with the numbers behind them.
  assert.match(text, /three builds run at once/)
  assert.match(text, /load 7\.9 per 8 cores/)
  assert.match(text, /the test shard holds the lock too long/)
  // The command is a chip with one copy action, and the raw report is not in
  // the feed: it stays the agent's evidence behind Details.
  const chip = need(target.querySelector('code.command'), 'the command chip')
  assert.equal(chip.textContent, payload.nextCommand)
  assert.doesNotMatch(text, /1\. three builds/)
  need(target.querySelector('button'), 'the copy button').click()
  await Promise.resolve()
  assert.deepEqual(copied, [payload.nextCommand])
  web.unmount(mounted)
  target.remove()
  Reflect.deleteProperty(globalThis, 'navigator')
})

test('an engine alert names its remedy as a command chip', () => {
  const payload = fixtures['alert/engine']
  assert.ok(payload && 'remedy' in payload, 'the engine alert fixture is missing')
  const post = postFor('alert', 'engine', payload)
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.eventComponents.AlertCard, { target, props: { post, notice: post.notice, surface: 'feed' } })
  web.flushSync()
  const text = (target.textContent ?? '').trim()
  assert.match(text, /migration failed/)
  const chip = need(target.querySelector('code.command'), 'the remedy chip')
  assert.equal(chip.textContent, payload.remedy)
  assert.doesNotMatch(text, /only the engine creates/)
  web.unmount(mounted)
  target.remove()
})

test('an alert stored before the typed events still shows its line', () => {
  // The sealed alert family routes an old post to this card, and it has no
  // payload: without the legacy reader the feed would show only "Alert".
  const legacy = { id: 'msg_0123456789abcdef', ts: 1, kind: 'alert', text: 'Doctor report 7 (hook main-updated):\nmachine is overloaded' }
  assert.equal(web.rendererFor(legacy), web.eventComponents.AlertCard, 'the alert family lost its card')
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.eventComponents.AlertCard, { target, props: { post: legacy, surface: 'feed' } })
  web.flushSync()
  const text = (target.textContent ?? '').trim()
  assert.match(text, /machine is overloaded/)
  web.unmount(mounted)
  target.remove()
})

test('a review result card shows all four results on feed and DM', () => {
  const base = web.reviewEvent({ kind: 'review', payload: fixtures['review/result'] })
  assert.ok(base, 'the review result fixture is missing')
  const cases = [
    { payload: { ...base, kind: 'approve' as const }, wants: [/PR #542 approved/, /approved/] },
    { payload: { ...base, kind: 'approve-fast' as const, fastLand: true, note: 'fast landing', area: 'docs' }, wants: [/fast landing/, /approved/] },
    { payload: { ...base, kind: 'approve' as const, note: 'normal path' }, wants: [/normal path/, /approved/] },
    { payload: base, wants: [/changes requested/, /1 blocking/, /events\.go:42/] },
    { payload: { ...base, kind: 'stop-early' as const, stopReason: 'superseded' as const, findings: [], blocking: 0, nits: 0 }, wants: [/stopped early/, /newer review replaced this one/] },
  ]
  for (const surface of ['feed', 'dm'] as const) {
    for (const c of cases) {
      const post = postFor('review', 'result', c.payload, 'PR #542 at head: reviewer result changes.')
      const target = document.createElement('div')
      document.body.append(target)
      const mounted = web.mount(web.eventComponents.ReviewResultCard, { target, props: { post, notice: post.notice, surface } })
      web.flushSync()
      const text = (target.textContent ?? '').trim()
      for (const want of c.wants) assert.match(text, want, `${c.payload.kind} on ${surface}: ${text}`)
      // The glyph is a drawn icon, never an emoji.
      assert.ok(target.querySelector('svg'), `${c.payload.kind} lost its icon`)
      if (surface === 'dm') assert.match(text, /Details/, `${c.payload.kind} DM lost its details toggle`)
      web.unmount(mounted)
      target.remove()
    }
  }
})

// renderMergeCard mounts the merge card of one typed payload and returns its text.
function renderMergeCard(payload: EventPayload): string {
  const post = postFor('merge', 'failed', payload)
  const target = document.createElement('div')
  document.body.append(target)
  const mounted = web.mount(web.eventComponents.MergeCard, { target, props: { post, notice: post.notice, surface: 'feed' } })
  web.flushSync()
  const text = (target.textContent ?? '').trim()
  web.unmount(mounted)
  target.remove()
  return text
}

test('a failed merge card says its failing checks once and names every member PR', () => {
  const base = web.mergeEvent({ kind: 'merge', notice: { kind: 'failed' }, payload: fixtures['merge/failed'] })
  assert.ok(base, 'the failed merge fixture is missing')
  const batch = {
    ...base,
    prs: [
      { number: 350, title: 'Close the issue once', url: 'https://github.com/acme/app/pull/350' },
      { number: 352, title: 'Tidy the notice', url: 'https://github.com/acme/app/pull/352' },
    ],
    checks: { done: 2, total: 2, failed: ['go-test'] },
    reason: undefined,
    reasonKind: 'checks-failed' as const,
  }
  const text = renderMergeCard(batch)
  assert.match(text, /2 PRs: #350, #352 into main/, `the title did not name the batch: ${text}`)
  assert.equal(text.match(/go-test/g)?.length, 1, `the failed check shows more than once: ${text}`)
  assert.doesNotMatch(text, /checks failed:/i, `the card repeated the cause: ${text}`)
  // A kind with no named check still says the cause, worded from the kind.
  const unnamed = renderMergeCard({ ...batch, checks: { done: 2, total: 2 } })
  assert.match(unnamed, /Checks failed\./, `the kind did not word the cause: ${unnamed}`)
  // A cause a person wrote, such as root's, still shows in full.
  const returned = renderMergeCard({ ...base, type: 'returned', reason: 'root sent it back to fix the flake', reasonKind: 'root-return' })
  assert.match(returned, /root sent it back to fix the flake/, `a free reason vanished: ${returned}`)
})

// A fact about one member of a batch, such as root returning that PR or its
// conflict with another member, names that PR. It must not claim the whole
// batch, which carries every member in its payload.
test('a member-scoped merge card names that PR, not the whole batch', () => {
  const base = web.mergeEvent({ kind: 'merge', notice: { kind: 'returned' }, payload: fixtures['merge/returned'] })
  assert.ok(base, 'the returned merge fixture is missing')
  const members = [
    { number: 1, title: 'First member', url: 'https://github.com/acme/app/pull/1' },
    { number: 2, title: 'Second member', url: 'https://github.com/acme/app/pull/2' },
  ]
  const cases = [
    { type: 'returned' as const, reasonKind: 'root-return' as const, wants: /Returned by root: #2 into main/ },
    { type: 'conflict' as const, reasonKind: 'conflict' as const, wants: /Merge conflict: #2 into main/ },
  ]
  for (const c of cases) {
    const text = renderMergeCard({ ...base, ...c, prs: members, member: 2, label: 'PR #2', reason: 'named member' })
    assert.match(text, c.wants, `${c.type} named the wrong subject: ${text}`)
    assert.doesNotMatch(text, /#1/, `${c.type} claimed the other member: ${text}`)
    assert.match(text, /batch of 2/, `${c.type} hid the batch: ${text}`)
    assert.match(text, /Second member/, `${c.type} named the other member's title: ${text}`)
    assert.doesNotMatch(text, /First member/, `${c.type} kept the other member's title: ${text}`)
  }
  // A published prefix leaves the failed member out of the trial's own list.
  // The card names it by its typed number and points at no other PR.
  const missing = renderMergeCard({ ...base, type: 'failed', prs: [members[0]], member: 2, reason: 'failed after the prefix', reasonKind: 'other' })
  assert.match(missing, /Merge failed: #2 into main/, `an unlisted member lost its number: ${missing}`)
  assert.doesNotMatch(missing, /#1|First member|Open PR/, `an unlisted member fell back to another PR: ${missing}`)
})

test('a single-PR merge card keeps the singular title', () => {
  const base = web.mergeEvent({ kind: 'merge', notice: { kind: 'landed' }, payload: fixtures['merge/landed'] })
  assert.ok(base, 'the landed merge fixture is missing')
  const text = renderMergeCard(base)
  assert.match(text, /Merged #242 into main/, `a one-PR title lost its shape: ${text}`)
  const direct = renderMergeCard({ ...base, reasonKind: 'tested-head' as const })
  assert.match(direct, /Landed without a trial: this head contains the base tip and passed its required CI\./)
})
