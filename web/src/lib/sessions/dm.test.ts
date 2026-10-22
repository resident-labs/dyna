import test from 'node:test'
import assert from 'node:assert/strict'
import { speaker } from '../ui/posts.js'
import { postedTo, turnHead, turnTail, activitySummary, dmView, groupMessages, sameText, sessionAddress, threadKeys, threadList, plain, voiceRequest, type DmItem, type DmRow, type DmSession, type Receipt, type Thread, type ThreadPost } from './dm.ts'
import type { Attachment } from '../api/room.ts'
import type { ShellJob } from '../api/terminal.ts'
import { need } from '../../../test/need.ts'
import { expectKind, isKind } from '../../../test/session-fixtures.ts'
const room = { id: 'r', name: 'room' }
const address = 'room:lead'
const ts = (s: number) => new Date(Date.UTC(2026, 9, 17, 16, 0, s)).toISOString()
const post = (id: string, from: string, to: string[], s: number, extra: Partial<ThreadPost> = {}): ThreadPost => ({ id, from, to, ts: ts(s), text: id, ...extra })
const att = (id: string, extra: Partial<Attachment> = {}): Attachment => ({ id, name: id, mime: 'application/octet-stream', size: 0, sha256: '', ...extra })

test('old servers have no posts or explicit address', () => {
  const session = { member: { name: 'lead' }, turns: [] }
  assert.equal(sessionAddress(session, room), address)
  assert.deepEqual(dmView(session, room), { address, rows: [], threads: [] })
})
test('thread keys remove owner, self and duplicate recipients; incoming keys use sender', () => {
  assert.deepEqual(threadKeys(post('a', address, ['room', address, 'room:peer', 'room:peer', 'other:peer'], 0), address, room.name), ['room:peer', 'other:peer'])
  assert.deepEqual(threadKeys(post('a', 'room:peer', [address], 0), address, room.name), ['room:peer'])
  assert.deepEqual(threadKeys(post('a', 'room', [address], 0), address, room.name), [])
})
test('consecutive posts share a chip; a later post starts a new chip at the bottom; mixed owner posts retain cards inline', () => {
  const attachments = [att('card', { render: 'html' })]
  const session = { address, turns: [{ n: 1, started: ts(0) }, { n: 2, started: ts(60) }], posts: [
    post('first', address, ['room:peer'], 10), post('second', 'room:peer', [address], 20),
    post('third', address, ['room', 'room:peer'], 70, { attachments }),
    post('root', address, ['room'], 80),
    post('fourth', 'room:peer', [address], 90),
  ] }
  const dm = dmView(session, room, {}, [{ id: 'fourth', from: 'room:peer', text: 'fourth', ts: ts(90), state: 'waiting' }])
  assert.equal(dm.threads.length, 1)
  assert.equal(need(dm.threads[0]).posts.length, 4)
  const order = dm.rows.map((r) => r.kind === 'thread' ? `chip:${(r.posts ?? []).map((p) => p.id)}${r.queued ? ':queued' : ''}` : r.kind === 'message' ? r.post?.id : r.kind)
  assert.deepEqual(order, ['turn', 'chip:first,second', 'turn', 'third', 'chip:third', 'root', 'chip:fourth:queued'])
  assert.equal(new Set(dm.rows.map((r) => r.key)).size, dm.rows.length)
  assert.equal(need(dm.rows.filter(isKind('message')).find((r) => r.post?.id === 'third')?.post).attachments, attachments)
})
test('another agent\'s post stays in its thread through queued, steering, and prompt delivery', () => {
  const peer = 'room:peer'
  const p = post('p1', peer, [address], 10)
  const q = (state: string): Receipt => ({ id: 'p1', from: peer, text: 'p1', ts: ts(10), state })
  const input = (kind: string, delivered: boolean): DmItem => ({ kind, postId: 'p1', from: peer, text: 'p1', ts: ts(12), delivered })
  const cases: Record<string, [DmSession, Receipt[]]> = {
    queued: [{ address, turns: [{ n: 1, started: ts(0) }], posts: [p] }, [q('waiting')]],
    'queued, no stored post': [{ address, turns: [{ n: 1, started: ts(0) }], posts: [] }, [q('waiting')]],
    delivering: [{ address, turns: [{ n: 1, started: ts(0) }], posts: [p] }, [q('delivering')]],
    steering: [{ address, turns: [{ n: 1, started: ts(0), items: [input('steering', true)] }], posts: [p] }, [q('delivered')]],
    prompt: [{ address, turns: [{ n: 2, started: ts(11), items: [input('prompt', true)] }], posts: [p] }, []],
  }
  for (const [name, [session, queue]] of Object.entries(cases)) {
    const dm = dmView(session, room, {}, queue)
    assert.deepEqual(dm.rows.filter(isKind('message')).map((r) => r.author), [], name)
    assert.deepEqual(dm.rows.filter(isKind('thread')).map((r) => [r.thread?.key, r.posts.length, r.queued]), [[peer, 1, name.startsWith('queued')]], name)
  }
})
test('peer inputs without stored posts keep each post ID and prefer receipt text and time', () => {
  const peer = 'room:peer'
  const receipt = (id: string, text: string, s: number, state: string): Receipt => ({ id, from: peer, text, ts: ts(s), state })
  const steer = (postId: string, text: string, s: number): DmItem => ({ kind: 'steering', postId, from: peer, text, ts: ts(s), delivered: true })
  const texts = (session: DmSession, queue: Receipt[]) => need(dmView({ address, posts: [], ...session }, room, {}, queue).threads.find((t) => t.key === peer)).posts.map((p) => [p.id, p.text, p.ts])
  assert.deepEqual(texts({ turns: [] }, [receipt('p1', 'Please check', 1, 'waiting'), receipt('p2', 'Please check again', 2, 'waiting')]),
    [['p1', 'Please check', ts(1)], ['p2', 'Please check again', ts(2)]])
  assert.deepEqual(texts({ turns: [{ n: 1, started: ts(0), items: [steer('p1', 'Same words', 3), steer('p2', 'Same words', 4)] }] }, []),
    [['p1', 'Same words', ts(3)], ['p2', 'Same words', ts(4)]])
  for (const state of ['delivering', 'delivered']) {
    assert.deepEqual(texts({ turns: [{ n: 1, started: ts(0), items: [steer('p1', 'Please ch…', 9)] }] }, [receipt('p1', 'Please check the full log', 5, state)]),
      [['p1', 'Please check the full log', ts(5)]], state)
  }
})
test('a Dyna instructions notice is a note in the DM, not an agent thread', () => {
  const note = post('ins', 'dyna', [address], 5, { kind: 'instructions', text: '[DYNA INSTRUCTIONS CHANGED] Since this conversation started, your standing instructions changed.' })
  const session = { address, turns: [{ n: 2, status: 'ok', started: ts(10), ended: ts(20), items: [{ kind: 'prompt', postId: 'ins', from: 'dyna', text: note.text, ts: ts(10) }] }], posts: [note] }
  const dm = dmView(session, room)
  assert.deepEqual(dm.threads, [])
  assert.deepEqual(dm.rows.filter((r) => r.kind !== 'turn' && r.kind !== 'turnEnd').map((r) => [r.kind, r.key, r.text]), [['instructions', 'post:ins', note.text]])
})
test('thread posts carry their delivery state for the thread panel', () => {
  const peer = 'room:peer'
  const session = { address, posts: [post('p1', peer, [address], 1), post('p2', peer, [address], 2)],
    turns: [{ n: 1, started: ts(0), items: [{ kind: 'steering', postId: 'p1', from: peer, text: 'p1', ts: ts(3), delivered: true }] }] }
  const queue = [{ id: 'p2', from: peer, text: 'p2', ts: ts(2), state: 'waiting' }, { id: 'p3', from: peer, text: 'p3', ts: ts(4), state: 'delivering' }]
  const thread = need(dmView(session, room, {}, queue).threads.find((t) => t.key === peer))
  assert.deepEqual(thread.posts.map((p) => [p.id, p.delivery, p.pending?.id]), [['p1', 'read', undefined], ['p2', 'queued', 'p2'], ['p3', 'delivered', 'p3']])
  assert.equal(need(session.posts[1]).delivery, undefined, 'stored posts stay unchanged')
})
test('a turn reply that repeats the partner\'s post to the owner folds under that post', () => {
  const result = 'Fixed the parser bug. **PR #12** is open, and CI is green.'
  const session = { address, turns: [
    { n: 3, status: 'ok', started: ts(0), ended: ts(30), items: [{ kind: 'reply', text: 'Fixed the parser bug.  **PR #12** is open,\nand CI is green.', ts: ts(30) }] },
    { n: 4, status: 'ok', started: ts(40), ended: ts(50), items: [{ kind: 'reply', text: 'Fixed the parser bug. PR #12 is open, and CI is not green.', ts: ts(50) }] },
    { n: 5, status: 'ok', started: ts(60), ended: ts(70), items: [{ kind: 'reply', text: result, ts: ts(70) }] },
    { n: 6, status: 'ok', started: ts(80), ended: ts(90), items: [{ kind: 'reply', text: 'Report: [results](https://example.com/new)', ts: ts(90) }] },
  ], posts: [post('result', address, ['room'], 20, { turn: 3, text: result }), post('fix', address, ['room'], 45, { turn: 4, text: result }), post('old', address, ['room'], 65, { turn: 4, text: result }),
    post('link', address, ['room'], 85, { turn: 6, text: 'Report: [results](https://example.com/old)' })] }
  const rows = dmView(session, room).rows.filter(isKind('message'))
  // Only whitespace may differ; a correction or a new link target stays visible.
  assert.deepEqual(rows.map((r) => [r.key, !!r.folded]), [['post:result', false], ['item:3:0', true], ['post:fix', false], ['item:4:0', false], ['post:old', false], ['item:5:0', false], ['post:link', false], ['item:6:0', false]])
})
test('sameText ignores whitespace only', () => {
  assert.equal(sameText('Done.\n\nCI is  green.', ' Done. CI is green. '), true)
  for (const [a, b] of [
    ['CI is green.', 'CI is not green.'],
    ['Report: [results](../build-12 "Report")', 'Report: [results](../build/12 "Report")'],
    ['Report: [results](https://example.com/Build)', 'Report: [results](https://example.com/build)'],
    ['Changed `internal/pr`', 'Changed `internal-pr`'],
    ['**Done.**', 'Done.'],
  ]) assert.equal(sameText(a, b), false, `${a} | ${b}`)
  assert.equal(sameText('', ''), false)
})
test('activity folds adjacent tools while thoughts, failures, and steering remain visible', () => {
  const items = [
    { kind: 'prompt', from: 'room (human)', text: 'start', ts: ts(0) },
    { kind: 'tool', tool: 'read 6 files', ts: ts(1) },
    { kind: 'thought', text: 'thinking', ts: ts(2) },
    { kind: 'tool', tool: 'ran', target: 'npm test', failed: true, ts: ts(3) },
    { kind: 'reply', text: 'result', ts: ts(49) },
    { kind: 'steering', from: 'room (human)', text: 'change', ts: ts(50) },
    { kind: 'tool', tool: 'edited', ts: ts(51) }, { kind: 'error', text: 'failed', ts: ts(52) },
  ]
  const dm = dmView({ address, turns: [{ n: 1, started: ts(0), items }] }, room)
  assert.deepEqual(dm.rows.map((r) => r.kind), ['turn', 'message', 'activity', 'thought', 'activity', 'message', 'message', 'activity', 'error'])
  assert.equal(need(expectKind('activity', dm.rows[4]).items[0]).failed, true)
  assert.equal(activitySummary(expectKind('activity', dm.rows[2]).items, ts(49)), 'Worked for 48s · read 6 files')
  assert.equal(need(dm.rows[6]).itemKind, 'steering')
})
test('grouping uses author and a seven minute window and resets at turn boundaries', () => {
  const message = (author: string, s: number): DmRow => ({ kind: 'message', key: `${author}:${s}`, author, ts: ts(s) })
  const activity: DmRow = { kind: 'activity', key: 'activity', items: [] }
  const turnStart: DmRow = { kind: 'turn', key: 'turn', turn: { n: 1, started: ts(0) } }
  const rows = groupMessages([message(address, 0), activity, message(address, 60), message('room', 61), message('room', 481), turnStart, message('room', 482)])
  assert.deepEqual(rows.filter(isKind('message')).map((r) => r.cont), [false, true, false, false, false])
})
test('timeline merges posts by timestamp without mutating input and handles absent timestamps', () => {
  const posts = [post('later', address, ['room'], 40), post('early', address, ['room'], 10)]
  const session = { address, posts, turns: [{ n: 1, started: ts(0), omitted: true, items: [{ kind: 'reply', text: 'middle', ts: ts(20) }, { kind: 'note', text: 'note' }] }] }
  assert.deepEqual(dmView(session, room).rows.filter(isKind('message')).map((r) => r.text), ['early', 'middle', 'later'])
  assert.equal(need(posts[0]).id, 'later')
})

test('human steering and automatic owner prompts keep distinct headers', () => {
  const rows = groupMessages([
    { kind: 'message', author: room.name, human: false, ts: ts(0) },
    { kind: 'message', author: room.name, human: true, ts: ts(1) },
  ])
  assert.deepEqual(rows.map((r) => r.cont), [false, false])
})

test('plain strips markdown for a one-line chip', () => {
  assert.equal(plain('#697 merged as `13940e0d`. #692 can rebase.'), '#697 merged as 13940e0d. #692 can rebase.')
  assert.equal(plain('## Result\n> quoted #1'), 'Result quoted #1')
  assert.equal(plain('Pushed `3f2a91c`. **Tests** pass.\n\nSee [the PR](https://x)'), 'Pushed 3f2a91c. Tests pass. See the PR')
})

test('owner files show once below the adjacent prompt, including a waking post before the turn', () => {
  const attachments = [att('image', { mime: 'image/png' })]
  for (const seconds of [9, 11]) {
    const session = { address, turns: [{ n: 2, started: ts(10), items: [{ kind: 'prompt', from: 'room (human)', text: 'Check this image', ts: ts(10) }] }], posts: [post('files', room.name, [address], seconds, { owner: true, human: true, speaker: 'you', text: 'Check this image', attachments })] }
    const rows = dmView(session, room).rows.filter(isKind('message'))
    assert.equal(rows.length, 2)
    assert.equal(need(rows[0]).text, 'Check this image')
    const files = need(rows[1])
    assert.equal(files.text, '')
    assert.equal(files.attachmentOnly, true)
    assert.equal(files.human, true)
    assert.equal(files.cont, true)
    assert.equal(files.post?.attachments, attachments)
  }
})

test('owner files require an adjacent prompt within two minutes to share its header', () => {
  const prompt = { key: 'prompt', kind: 'message', itemKind: 'steering', author: room.name, human: true, ts: ts(0) }
  const files = { kind: 'message', author: room.name, human: true, attachmentOnly: true, attachmentPrompt: 'prompt', ts: ts(121) }
  assert.equal(need(groupMessages([prompt, files])[1]).cont, false)
  assert.equal(need(groupMessages([prompt, { kind: 'note' }, { ...files, ts: ts(1) }])[2]).cont, false)
  assert.equal(need(groupMessages([prompt, { ...files, ts: ts(120) }])[1]).cont, true)
})

test('unmatched owner attachment posts keep their instructions and their own header', () => {
  const text = 'Please inspect the attached failure log'
  const attachments = [att('log', { mime: 'text/plain' })]
  const ownerPost = post('files', room.name, [address], 30, { owner: true, text, attachments })
  const variants: DmItem[][] = [
    [], // No prompt yet, including paused delivery.
    [{ kind: 'prompt', from: 'room (human)', text: 'An unrelated earlier request', ts: ts(0) }],
    [{ kind: 'steering', from: 'room (human)', text: 'An unrelated later request', ts: ts(31) }],
    [{ kind: 'prompt', from: 'room:peer', text, ts: ts(0) }],
  ]
  for (const items of variants) {
    const dm = dmView({ address, posts: [ownerPost], turns: [{ n: 1, started: ts(0), omitted: !items.length, items }] }, room)
    const files = need(dm.rows.filter(isKind('message')).find(r => r.post?.id === 'files'))
    assert.equal(files.text, text)
    assert.equal(files.attachmentOnly, false)
    assert.equal(files.cont, false)
    assert.equal(files.post?.attachments, attachments)
    const later = dm.rows.findIndex(r => r.text === 'An unrelated later request')
    if (later >= 0) assert.ok(dm.rows.indexOf(files) < later)
  }
})

test('pending owner text disappears only after the matching steering message arrives', () => {
  const text = 'Please inspect the attached failure log'
  const ownerPost = post('files', room.name, [address], 30, { owner: true, human: true, text, attachments: [att('log')] })
  const session: DmSession = { address, posts: [ownerPost], turns: [] }
  assert.equal(need(dmView(session, room).rows[0]).text, text)
  session.turns = [{ n: 1, started: ts(0), items: [{ kind: 'steering', from: 'room (human)', text, ts: ts(30) }] }]
  const rows = dmView(session, room).rows.filter(isKind('message'))
  assert.deepEqual(rows.map(r => r.text), [text, ''])
  assert.equal(need(rows[1]).cont, true)
  assert.equal(ownerPost.text, text)
})


test('numeric room post timestamps stay between turn items', () => {
  const session = { address, turns: [{ n: 1, started: ts(0), items: [
    { kind: 'reply', text: 'before', ts: ts(10) },
    { kind: 'reply', text: 'after', ts: ts(30) },
  ] }], posts: [post('between', address, ['room'], 20, { ts: Date.parse(ts(20)) })] }
  const rows = dmView(session, room).rows
  assert.equal(need(rows[0]).kind, 'turn')
  assert.deepEqual(rows.filter(isKind('message')).map(r => r.text), ['before', 'between', 'after'])
})

test('every peer message folds into that agent\'s thread; the newest gets a chip where it arrived', () => {
  const item = (kind: string, from: string, text: string, s: number): DmItem => ({ kind, from, text, ts: ts(s) })
  const session = { address, turns: [{ n: 1, started: ts(0), items: [
    item('prompt', 'room', 'Owner task', 1),
    item('steering', 'room:peer', 'older steer', 10),
    item('reply', '', 'working', 20),
    item('steering', 'room:peer', 'newest steer', 30),
    item('steering', 'room:other', 'from another', 40),
    item('steering', 'room (human)', 'human words', 50),
  ] }], posts: [post('p1', 'room:peer', [address], 15, { text: 'older steer' })] }
  const dm = dmView(session, room)
  assert.deepEqual(dm.rows.filter(isKind('message')).map((r) => r.text), ['Owner task', 'working', 'human words'])
  assert.deepEqual(dm.rows.filter(isKind('thread')).map((r) => [r.thread?.key, (r.posts ?? []).map((p) => p.text)]), [['room:peer', ['older steer']], ['room:peer', ['newest steer']], ['room:other', ['from another']]])
  const peer = need(dm.threads.find((t) => t.key === 'room:peer'))
  assert.deepEqual(peer.posts.map((p) => p.text), ['older steer', 'newest steer'])
  assert.equal(peer.posts.at(-1)?.text, 'newest steer')
})
test('the thread list puts agents newest first and folds shell jobs into a group', () => {
  const thread = (key: string, s: number): Thread => ({ key, posts: [post(key, key, [address], s)] })
  const { recent, shells } = threadList([thread('room:old', 1), thread('shell:sh_a', 2), thread('room:new', 5), thread('shell:sh_b', 4)])
  assert.deepEqual(recent.map((t) => t.key), ['room:new', 'room:old'])
  assert.deepEqual(shells.map((t) => t.key), ['shell:sh_b', 'shell:sh_a'])
})
test('the thread list gives every child a row and keeps child threads out of recent', () => {
  const thread = (key: string, s: number): Thread => ({ key, posts: [post(key, key, [address], s)] })
  const kids = [{ name: 'quiet', address: 'room:quiet' }, { name: 'busy', address: 'room:busy' }, { name: 'asleep', address: 'room:asleep' }]
  const { recent, children } = threadList([thread('room:peer', 9), thread('room:busy', 3)], kids)
  assert.deepEqual(recent.map((t) => t.key), ['room:peer'])
  assert.deepEqual(children.map((c) => [c.member.name, c.thread?.key]), [['busy', 'room:busy'], ['asleep', undefined], ['quiet', undefined]])
})
test('activity summary reads like the turn divider and skips a zero duration', () => {
  const tool = (s: number, name = 'Read'): Pick<DmRow, 'tool' | 'ts'> => ({ tool: name, ts: ts(s) })
  assert.equal(activitySummary([tool(0), tool(10)], new Date(Date.UTC(2026, 9, 17, 16, 4, 0)).toISOString()), 'Worked for 4m00s · read 2 files')
  assert.equal(activitySummary([tool(5, 'Edit')]), 'edited 1 file')
})

test('a finished turn ends with a footer row at its end time; a running turn has none', () => {
  const session = { address, turns: [
    { n: 1, status: 'ok', started: ts(0), ended: ts(30), items: [{ kind: 'reply', text: 'done', ts: ts(30) }] },
    { n: 2, status: 'running', started: ts(40), items: [{ kind: 'reply', text: 'working', ts: ts(45) }] },
  ], posts: [post('report', address, ['room'], 35)] }
  const rows = dmView(session, room).rows
  assert.deepEqual(rows.map((r) => r.key), ['turn:1', 'item:1:0', 'turn-end:1', 'post:report', 'turn:2', 'item:2:0'])
  assert.equal(need(rows[2]).kind, 'turnEnd')
  assert.equal(expectKind('turnEnd', rows[2]).turn.n, 1)
  assert.equal(need(rows[3]).cont, false, 'a message after a turn footer starts a new group')
})
test('the turn divider text keeps spaces around its separators', () => {
  const turn = { n: 1, started: '2026-10-18T10:00:00Z', ended: '2026-10-18T10:01:00Z', profile: 'sol', model: 'gpt', usage: { inputTokens: 20, outputTokens: 10 } }
  assert.equal(turnHead(turn), 'Turn 1 · sol / gpt')
  assert.equal(turnHead({ n: 2, profile: 'sol' }), 'Turn 2 · sol')
  assert.equal(turnHead({ n: 3 }), 'Turn 3')
  assert.equal(turnTail(turn), '1m00s · 30 tokens')
  assert.doesNotMatch(turnTail({ ...turn, usage: null }), /tokens/)
})

test('the root DM is the owner conversation inline; agent reports and root steering are threads', () => {
  const owner = 'room', root = 'room:root'
  const posts = [
    post('ask', owner, [root], 1, { human: true }),
    post('answer', owner, [owner], 2), // an external root posts as the room, not human
    post('report1', 'room:lead', [owner], 3),
    post('report2', 'room:worker', [owner], 4),
    post('steer', owner, ['room:lead'], 5), // root steers an agent
    post('keeper', 'goal-keeper', [owner], 6),
  ]
  const view = dmView({ address: root, member: { name: 'root' }, posts, turns: [] }, { name: owner })
  const inline = view.rows.filter(isKind('message')).map((r) => r.post?.id)
  assert.deepEqual(inline, ['ask', 'answer'], 'the main stream is the owner conversation')
  assert.deepEqual(view.threads.map((t) => t.key).sort(), ['goal-keeper', 'room:lead', 'room:worker'])
  assert.deepEqual(need(view.threads.find((t) => t.key === 'room:lead')).posts.map((p) => p.id), ['report1', 'steer'])
})

test('an agent still sees the owner steering it inline, not as a thread', () => {
  assert.deepEqual(threadKeys(post('a', 'room', ['room:lead'], 0), 'room:lead', 'room'), [])
})


test('the root DM labels its rows you and root, and drops the posted-to chip that is true of every row', () => {
  const owner = 'room', root = 'room:root'
  const posts = [post('ask', owner, [root], 1, { human: true }), post('answer', owner, [owner], 2), post('steer', owner, ['room:lead'], 3)]
  const view = dmView({ address: root, member: { name: 'root' }, posts, turns: [] }, { name: owner })
  const rows = view.rows.filter(isKind('message'))
  assert.deepEqual(rows.map((r) => speaker(r.post, owner)), ['you', 'root'], 'the owner is you; the room address that is not the human is root')
  assert.equal(postedTo(need(need(rows[0]).post), root, owner), '', 'every root DM row is to root')
  assert.equal(postedTo(need(need(rows[1]).post), root, owner), '')
  // Where it adds information it stays: in an agent DM, and in the root DM for a post to someone else.
  assert.equal(postedTo({ to: [owner] }, 'room:lead', owner), '#room · @root')
  assert.equal(postedTo({ to: ['room:lead', owner] }, root, owner), '#room · @lead, @root')
})

test('a compaction divider never folds into the activity block around it', () => {
  const items = [
    { kind: 'tool', tool: 'ran', target: 'go test ./...', ts: ts(1) },
    { kind: 'compaction', state: 'started', ts: ts(2) },
    { kind: 'compaction', state: 'completed', trigger: 'auto', ts: ts(3) },
    { kind: 'tool', tool: 'ran', target: 'go vet ./...', ts: ts(4) },
  ]
  const session = { member: { name: 'lead' }, turns: [{ n: 1, status: 'running', started: ts(1), items }] }
  const rows = dmView(session, room).rows
  assert.deepEqual(rows.map((r) => r.kind), ['turn', 'activity', 'compaction', 'compaction', 'activity'])
  assert.deepEqual(rows.filter(isKind('activity')).map((r) => r.items.length), [1, 1])
})

test('a compaction divider is one row that the compaction setting hides', () => {
  const items = [
    { kind: 'reply', text: 'before', ts: ts(1) },
    { kind: 'compaction', state: 'completed', trigger: 'auto', tokensBefore: 182000, tokensAfter: 41000, ts: ts(2) },
    { kind: 'reply', text: 'after', ts: ts(3) },
  ]
  const session = { member: { name: 'lead' }, turns: [{ n: 1, status: 'running', started: ts(1), items }] }
  const rows = (show: { compaction?: boolean }) => dmView(session, room, show).rows
  assert.deepEqual(rows({}).map((r) => r.kind), ['turn', 'message', 'compaction', 'message'], 'the divider keeps its own row between messages')
  assert.deepEqual(rows({ compaction: false }).map((r) => r.kind), ['turn', 'message', 'message'])
  assert.equal(need(rows({}).find((r) => r.kind === 'compaction')).key, 'item:1:1')
})
test('slow-test issues in a row fold into one note row; other notes stay apart', () => {
  const slow = (id: string, s: number) => post(id, 'room', ['room'], s, { kind: 'issue', text: `Slow test over the CI limit: x/Test${id}\n\nTime: 2 s, limit 1 s\nPRs: #1\nArea: x\n\nnote` })
  const ins = post('ins', 'dyna', [address], 3, { kind: 'instructions', text: '[DYNA INSTRUCTIONS CHANGED] x' })
  const session = { address, turns: [], posts: [slow('A', 1), slow('B', 2), ins, slow('C', 4)] }
  const rows = dmView(session, room).rows
  assert.deepEqual(rows.map((r) => [r.kind, (r.kind === 'instructions' ? r.notes ?? [] : []).map((n) => n.slow?.name)]), [['instructions', ['TestA', 'TestB']], ['instructions', []], ['instructions', ['TestC']]])
})

// Voice's request is the owner's words: it shows inline as one voice row in
// every delivery state, never as a Voice thread. The call's own lines are
// not requests.
test("Voice's request to root is one voice row, never a thread", () => {
  const voice = `${room.name}:voice-call`
  const p = { ...post('v1', voice, [room.name], 10), source: 'voice', text: 'Check CI on PR 7.' }
  const q = (state: string): Receipt => ({ id: 'v1', from: voice, text: 'Check CI on PR 7.', ts: ts(10), state })
  const input = (kind: string): DmItem => ({ kind, postId: 'v1', from: voice, text: 'Check CI on PR 7.', ts: ts(12), delivered: true })
  const cases: Record<string, [DmSession, Receipt[], string | undefined]> = {
    queued: [{ address, turns: [{ n: 1, started: ts(0) }], posts: [p] }, [q('waiting')], 'queued'],
    'queued, no stored post': [{ address, turns: [{ n: 1, started: ts(0) }], posts: [] }, [q('waiting')], 'queued'],
    prompt: [{ address, turns: [{ n: 2, started: ts(11), items: [input('prompt')] }], posts: [p] }, [], 'read'],
    'posted only': [{ address, turns: [], posts: [p] }, [], undefined],
  }
  for (const [name, [session, queue, delivery]] of Object.entries(cases)) {
    const dm = dmView(session, room, {}, queue)
    assert.deepEqual(dm.rows.filter(isKind('voice')).map((r) => [r.text, r.delivery]), [['Check CI on PR 7.', delivery]], name)
    assert.deepEqual(dm.rows.filter((r) => r.kind === 'thread' || r.kind === 'message').map((r) => r.kind), [], name)
    assert.deepEqual(dm.threads.map((t) => t.key), [], name)
  }
  assert.equal(voiceRequest(p, room.name), true)
  assert.equal(voiceRequest({ ...p, kind: 'voice' }, room.name), false, 'a line of the call itself')
  assert.equal(voiceRequest({ ...p, from: 'room:peer' }, room.name), false)
})

test('a Dyna shell call is one terminal row; waits and stops fold into it', () => {
  const job: ShellJob = { id: 'sh_1', command: ['bash', '-c', 'make'], name: 'Build', cwd: '/', state: 'running', code: 0, started: ts(1) }
  const call = (s: number, extra: Partial<DmItem> = {}): DmItem => ({ kind: 'tool', ts: ts(s), tool: 'ran', target: 'make', ...extra })
  const session = { address, turns: [{ n: 1, started: ts(0), items: [
    call(1, { jobOp: 'run', jobId: 'sh_1', job }),
    call(2, { jobOp: 'output', jobId: 'sh_1' }),
    call(3, { jobOp: 'stop', jobId: 'sh_1' }),
    // Not on this page, not a Dyna job, and a run with no job: all stay as tool lines.
    call(4, { jobOp: 'output', jobId: 'sh_other' }),
    call(5, { tool: 'read', target: 'a.go' }),
    call(6, { jobOp: 'run' }),
  ] }] }
  const rows = dmView(session, room).rows
  const shells = rows.filter(isKind('shell'))
  assert.equal(shells.length, 1)
  const first = need(shells[0])
  assert.deepEqual([first.job?.id, first.waits, first.author], ['sh_1', 2, address])
  const lines = rows.filter(isKind('activity')).flatMap((r) => r.items)
  assert.deepEqual(lines.map((it) => it.target), ['make', 'a.go', 'make'])
  assert.equal(lines.some((it) => it.jobId === 'sh_1'), false)
})

test('three or more quiet shell commands in a row fold into one line; others never fold', () => {
  const job = (id: string, extra: Partial<ShellJob> = {}): ShellJob => ({ id, command: ['bash', '-c', id], name: id, cwd: '/', state: 'exited', code: 0, started: ts(1), ended: ts(2), ...extra })
  const run = (j: ShellJob): DmItem => ({ kind: 'tool', ts: ts(1), tool: 'ran', target: j.id, jobOp: 'run', jobId: j.id, job: j })
  const rowsOf = (jobs: ShellJob[], ...between: DmItem[]) => dmView({ address, turns: [{ n: 1, started: ts(0), items: [...jobs.slice(0, 2).map(run), ...between, ...jobs.slice(2).map(run)] }] }, room).rows.filter((r) => r.kind === 'shell' || r.kind === 'shellGroup')
  const quiet3 = [job('a'), job('b'), job('c')]
  const folded = rowsOf(quiet3)
  assert.deepEqual(folded.map((r) => [r.kind, r.kind === 'shellGroup' ? r.jobs.map((j) => j.id) : undefined]), [['shellGroup', ['a', 'b', 'c']]])
  assert.equal(need(folded[0]).author, address)
  // Two do not fold.
  assert.deepEqual(rowsOf(quiet3.slice(0, 2)).map((r) => r.kind), ['shell', 'shell'])
  // A failed, stopped, running or background job breaks the run and stays visible.
  for (const odd of [job('x', { code: 1 }), job('x', { code: -1, stopBy: 'agent' }), job('x', { state: 'running', ended: undefined }), job('x', { backgroundAt: ts(1) })]) {
    assert.deepEqual(rowsOf([job('a'), job('b'), odd, job('c'), job('d')]).map((r) => r.kind), ['shell', 'shell', 'shell', 'shell', 'shell'])
    assert.deepEqual(rowsOf([job('a'), job('b'), job('c'), odd]).map((r) => r.kind), ['shellGroup', 'shell'])
  }
  // Something between them, such as a reply, ends the run.
  const reply = { kind: 'reply', ts: ts(1), text: 'Looking at it.' }
  assert.deepEqual(rowsOf([job('a'), job('b'), job('c')], reply).map((r) => r.kind), ['shell', 'shell', 'shell'])
})

test('a job-end notice folds into the block of its call, and names the turn it woke', () => {
  const job: ShellJob = { id: 'sh_20261020-100000-aaaaaaaa', command: ['bash', '-c', 'make'], name: 'Build', cwd: '/', state: 'exited', code: 0, started: ts(1), ended: ts(9), backgroundAt: ts(2) }
  const other = { ...job, id: 'sh_20261020-100000-bbbbbbbb', name: 'Test' }
  const run = (j: ShellJob, s: number): DmItem => ({ kind: 'tool', ts: ts(s), tool: 'ran', target: j.name, jobOp: 'run', jobId: j.id, job: j })
  const notice = (j: ShellJob, s: number, id: string) => post(id, `shell:${j.id}`, [address], s, { completion: true, kind: 'completed' })
  const item = (kind: string, id: string, s: number): DmItem => ({ kind, ts: ts(s), postId: id, from: id === 'n1' ? `shell:${job.id}` : `shell:${other.id}`, text: 'done', delivered: true })
  const session = { address, turns: [
    { n: 1, started: ts(0), items: [run(job, 1), run(other, 1), item('steering', 'n2', 20)] },
    { n: 2, started: ts(30), items: [item('prompt', 'n1', 30)] },
  ], posts: [notice(job, 29, 'n1'), notice(other, 20, 'n2')] }
  const rows = dmView(session, room).rows
  const blocks = rows.filter(isKind('shell'))
  const byId = Object.fromEntries(blocks.map((r) => [r.job.id, r.finished]))
  assert.deepEqual(byId[job.id], { at: ts(29), turn: 2, woke: true })
  assert.deepEqual(byId[other.id], { at: ts(20), turn: 1, woke: false })
  // No chip for either notice; a mid-turn one leaves a pointer; a waking one names its turn.
  assert.equal(rows.filter(isKind('thread')).length, 0)
  assert.deepEqual(rows.filter(isKind('shellNote')).map((r) => r.job.id), [other.id])
  const turns = rows.filter(isKind('turn'))
  assert.equal(need(need(turns.find((r) => r.turn.n === 2)).wokeBy).id, job.id)
  assert.equal(need(turns.find((r) => r.turn.n === 1)).wokeBy, undefined)
})

test('a job-end notice whose call is not on the page stays a chip that names its job', () => {
  const id = 'sh_20261019-164000-cccccccc'
  const session = { address, turns: [{ n: 9, started: ts(0), items: [] }], posts: [post('old', `shell:${id}`, [address], 5, { completion: true, kind: 'completed' })] }
  const chips = dmView(session, room).rows.filter(isKind('thread'))
  assert.deepEqual(chips.map((c) => c.shell), [id])
})

test('a turn of calls that are not reads, runs, edits or searches still has a summary', () => {
  const items = [{ tool: 'messaged', target: '@root', failed: true, ts: ts(1) }]
  assert.equal(activitySummary(items, ts(1)), '1 other call · 1 failed')
  assert.equal(activitySummary([{ tool: 'ran', ts: ts(1) }, { tool: 'messaged', ts: ts(1) }], ts(1)), 'ran 1 command · 1 other call')
  assert.notEqual(activitySummary([{ tool: 'loaded tools', ts: ts(1) }], ts(1)), '')
})
