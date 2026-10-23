import { test } from 'node:test'
import assert from 'node:assert/strict'
import { feedModel, feedView, GENERAL, parentTrail, prepareFeedText, inView, homeView, type DraftParse } from './feedViews.ts'

const owner = { owner: true, name: 'repo', address: 'repo' }
const lead = { id: 'lead-id', name: 'lead', address: 'repo:lead' }
const peer = { id: 'peer-id', name: 'peer', address: 'repo:peer' }
const child = { id: 'child-id', name: 'child', address: 'repo:child', parent: lead.id }
const sibling = { id: 'sibling-id', name: 'sibling', address: 'repo:sibling', parent: lead.id }
const grandchild = { id: 'grandchild-id', name: 'grandchild', address: 'repo:grandchild', parent: child.id }
const model = feedModel([owner, lead, child, grandchild, sibling, peer])
const team = feedView(lead.id)

test('breadcrumbs follow the full hierarchy', () => {
  assert.deepEqual(model.trail(grandchild.id).map((m) => m.id), [lead.id, child.id, grandchild.id])
})
test('a session breadcrumb starts at root and lists each parent session', () => {
  const root = { id: 'root:room', rootSeat: true, name: 'root' }
  assert.deepEqual(parentTrail(model, grandchild.parent, root).map((m) => m.name), ['root', 'lead', 'child'])
  assert.deepEqual(parentTrail(model, undefined, root).map((m) => m.name), ['root'], 'a top-level session sits under root')
  // An internal root session already heads its children's trail.
  const rootSession = { id: 'root-id', name: 'root', role: 'root', address: 'repo:root' }
  const worker = { id: 'worker-id', name: 'worker', address: 'repo:worker', parent: rootSession.id }
  assert.deepEqual(parentTrail(feedModel([rootSession, worker]), worker.parent, root).map((m) => m.id), ['root-id'])
})

test('feed view drafts use the shared parser to preserve real recipients and session commands', async () => {
  const parse = async (text: string) => {
    const responses: Record<string, DraftParse & { self?: boolean }> = {
      'Please check': { default: true },
      'Please ask @peer': { default: false },
      'Please ask (@peer)': { default: false },
      '@root note': { default: false, self: true },
      '/sol task': { spawn: {} },
    }
    assert.ok(Object.hasOwn(responses, text))
    const response = responses[text]
    assert.ok(response)
    return response
  }
  assert.equal(await prepareFeedText('Please check', lead, model, parse), '@child @sibling Please check')
  assert.equal(await prepareFeedText('Please check', grandchild, model, parse), '@grandchild Please check')
  for (const text of ['Please ask @peer', 'Please ask (@peer)', '@root note', '/sol task']) {
    assert.equal(await prepareFeedText(text, lead, model, parse), text)
  }
  assert.equal(await prepareFeedText('Hello', null, model, () => assert.fail('General needs no extra parse')), 'Hello')
})
test('unknown, escaped, and code mentions retain feed view defaults when the parser finds no recipient', async () => {
  for (const text of ['Example @unknown', 'Example `@peer`', 'Example \\@peer', 'Example ``` @peer ```']) {
    const parse = async (value: string) => { assert.equal(value, text); return { default: true } }
    assert.equal(await prepareFeedText(text, lead, model, parse), '@child @sibling ' + text)
  }
})
test('a parser failure blocks preparation instead of falling back to top-level agents', async () => {
  await assert.rejects(prepareFeedText('Hello', lead, model, async () => { throw new Error('offline') }), /offline/)
})

test('only agents with direct children get a feed view at each depth', () => {
  assert.deepEqual(model.leads.map((m) => m.id), [lead.id, child.id])
  const leaves: (string | undefined)[] = [peer.id, sibling.id, grandchild.id]
  assert.equal(model.leads.some((m) => leaves.includes(m.id)), false)
})
test('saved channel links resolve to the room chat', () => {
  assert.equal(model.resolveView(feedView(grandchild.id)), GENERAL)
  assert.equal(model.resolveView(feedView(sibling.id)), GENERAL)
  assert.equal(model.resolveView(feedView(peer.id)), GENERAL)
  assert.equal(model.resolveView(feedView(lead.id)), GENERAL)
  assert.equal(model.resolveView(feedView('missing')), GENERAL)
  assert.equal(model.resolveView('files'), 'files')
})
test('closed child history keeps its parent feed view available', () => {
  const archived = feedModel([owner, lead, { ...child, closed: true }])
  assert.deepEqual(archived.leads.map((m) => m.id), [lead.id])
  assert.equal(archived.resolveView(feedView(lead.id)), GENERAL)
})

test('agent questions and answers use the same DM thread as posts', async () => {
  const { dmView } = await import('../sessions/dm.ts')
  const posts = [
    { id: 'ask', kind: 'message', from: child.address, to: [lead.address], ts: 1, text: '[QUESTION q1] Which fix?' },
    { id: 'answer', kind: 'message', from: lead.address, to: [child.address], ts: 2, text: '[ANSWER q1] Use the small fix.' },
  ]
  for (const [session, other] of [[child, lead], [lead, child]] satisfies [{ address: string }, { address: string }][]) {
    const dm = dmView({ address: session.address, posts }, { name: owner.address })
    assert.equal(dm.threads.length, 1)
    const thread = dm.threads[0]
    assert.ok(thread)
    assert.equal(thread.key, other.address)
    assert.deepEqual(thread.posts.map((p) => p.id), ['ask', 'answer'])
  }
  const first = posts[0]
  assert.ok(first)
  const ownerQuestion = { ...first, to: [owner.address] }
  assert.ok(dmView({ address: child.address, posts: [ownerQuestion] }, { name: owner.address }).rows.some((r) => 'post' in r && r.post?.id === 'ask'))
})

test('feed posts use the feed views the server routed them to, the sender home first', () => {
  const post = { views: [feedView('lead'), GENERAL] }
  assert.equal(inView(post, feedView('lead')), true)
  assert.equal(inView(post, GENERAL), true)
  assert.equal(inView(post, feedView('peer')), false)
  assert.equal(homeView(post), feedView('lead'))
  // A post without server routing, such as a transcript post, shows in general.
  assert.equal(inView({}, GENERAL), true)
  assert.equal(homeView({}), GENERAL)
})

test('voice call lines stay out of the room chat and have no feed view', async () => {
  const { VOICE, isFeedView } = await import('./feedViews.ts')
  assert.equal(isFeedView(VOICE), false)
  assert.equal(inView({ views: [VOICE] }, GENERAL), false)
  assert.equal(inView({ views: [VOICE] }, VOICE), true)
})
