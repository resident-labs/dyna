import test from 'node:test'
import assert from 'node:assert/strict'
import { rm } from 'node:fs/promises'
import { bundle, domNames, importBundle, installDom } from './prDetailHarness.ts'
import { issueReference, issueWork, roomIssueLabel, unifiedIssues, type RoomIssue } from './unifiedIssues.ts'
import { issueGroups, issueChip } from './github.ts'
import type { GitHubItem } from '../api/github.ts'
import { element, elements, need } from '../../../test/need.ts'

const settle = () => new Promise((r) => setTimeout(r, 0))
// A new test settles Svelte updates and API promises without a timer, as r2
// asks. The older tests in this file keep their timer helper.
const microtasks = async () => { for (let turn = 0; turn < 8; turn++) await Promise.resolve() }
const issue = (number: number, group: string, extra: Partial<GitHubItem> = {}): GitHubItem => ({ number, title: `Issue ${number}`, state: group === 'closed-today' || group === '' ? 'CLOSED' : 'OPEN', group, workingSessionLive: false, author: { login: 'sam' }, labels: [], createdAt: '2026-10-19T00:00:00Z', updatedAt: '2026-10-19T00:00:00Z', url: `https://github.com/o/r/issues/${number}`, ...extra })

test('issueGroups orders the groups and puts older closed issues in Closed', () => {
  const items = [issue(1, 'has-pr'), issue(2, 'needs-owner'), issue(3, ''), issue(4, 'working'), issue(5, 'closed-today')]
  const groups = issueGroups(items)
  assert.deepEqual(groups.map((g) => g.label), ['Needs an owner', 'An agent is working', 'Has a PR open', 'Closed today', 'Closed'])
  assert.deepEqual(groups.map((g) => g.rows.map((r) => r.number)), [[2], [4], [1], [5], [3]])
  assert.deepEqual(issueGroups([issue(2, 'needs-owner')]).map((g) => g.id), ['needs-owner'])
})

const API = `export const api = {
  feedAround: () => Promise.resolve({ posts: [{ id: 'msg_local', text: 'Local body text' }] }),
  githubList: () => globalThis.issueListFails ? Promise.reject(new Error('No repository')) : Promise.resolve({ repo: 'o/r', items: globalThis.issueItems }),
  githubView: (_r, _k, n) => globalThis.issueViewFails ? Promise.reject(new Error('Issue not found')) : Promise.resolve({ number: n, title: 'Issue ' + n, ...globalThis.issueItems.find((i) => i.number === n), body: 'Body text', comments: [] }),
  mergeControlStatus: () => { globalThis.issueMergeCalls++; return Promise.resolve({ roomId: 'r', paused: false, queue: [], trials: [], localCIChecks: {} }) },
}; export const openExternal = () => {}`

// Bundle outside the test clock: only the mounted view is measured.
const listCode = await bundle({ component: 'GitHub', api: API })
const pageCode = await bundle({ component: 'IssuePage', api: API })

async function render(items: GitHubItem[], props: object = {}, wait = settle) {
  const dom = installDom({ ResizeObserver: class { observe() {} unobserve() {} disconnect() {} }, issueItems: items, issueMergeCalls: 0 })
  dom.reconfigure({ url: 'http://localhost/' })
  Object.assign(globalThis, { history: dom.window.history, location: dom.window.location })
  const { dir, default: GitHub, mount } = await importBundle(listCode, 'GitHub')
  const target = need(document.getElementById('app'), '#app')
  mount(GitHub, { target, props: { roomId: 'r', kind: 'issue', ...props } })
  await wait()
  await wait()
  return { target, done: () => { dom.window.close(); for (const n of [...domNames, 'history', 'location', 'issueMergeCalls', 'issueItems', 'issueViewFails', 'issueListFails']) Reflect.deleteProperty(globalThis, n); return rm(dir, { recursive: true, force: true }) } }
}

test('the issue page hands its reference to the room composer', async () => {
  const references: unknown[] = []
  const r = await render([issue(839, 'needs-owner')], { onmention: (reference: unknown) => references.push(reference) })
  try {
    element(r.target, '.row').click()
    await settle()
    assert.equal(r.target.querySelector('.list'), null)
    need(elements(r.target, 'button').find((b) => b.textContent === 'Mention in chat')).click()
    assert.deepEqual(references, [{ kind: 'issue', number: 839, title: 'Issue 839', state: 'OPEN', url: 'https://github.com/o/r/issues/839' }])
  } finally {
    await r.done()
  }
})

test('the Has a PR open chip names the open PR, not an older closed one', () => {
  const it = issue(836, 'has-pr', { linkedPRs: [{ number: 8, state: 'CLOSED', isDraft: false, url: 'u8' }, { number: 9, state: 'OPEN', isDraft: true, url: 'u9' }] })
  assert.equal(issueChip(it)?.label, 'PR #9 draft')
})

test('the issue page has the shared facts line and a Work on this issue card that opens the session and the PR', async () => {
  const dom = installDom()
  const { dir, default: IssuePage, mount } = await importBundle(pageCode, 'IssuePage')
  const calls: string[] = []
  const it = issue(842, 'working', { workingSession: 's1', workingSessionName: 'ui-feed-jump', workingSessionLive: true, linkedPRs: [{ number: 852, state: 'OPEN', isDraft: true, url: 'u852' }], labels: [{ name: 'bug', color: 'd73a4a' }] })
  const target = need(document.getElementById('app'), '#app')
  mount(IssuePage, { target, props: { it, detail: { ...it, body: 'Body', comments: [] }, onsession: () => calls.push('session'), onpr: (n: number) => calls.push(`pr${n}`) } })
  await settle()
  try {
    const facts = element(target, '.factsline')
    assert.equal(element(facts, '.author').textContent, 'sam')
    assert.match(facts.textContent, /bug/)
    assert.match(element(target, 'h4').textContent, /Work on this issue\s*2/)
    for (const b of elements(target, 'button')) {
      if (/^Session ui-feed-jump/.test(b.textContent) || b.textContent === 'PR #852') b.click()
    }
    assert.deepEqual(calls, ['session', 'pr852'])
    assert.equal(element(target, '.name + span').textContent, 'Working')
  } finally {
    dom.window.close()
    for (const n of domNames) Reflect.deleteProperty(globalThis, n)
    await rm(dir, { recursive: true, force: true })
  }
})


test('opening an issue records its link; Back and Forward follow issue links', async () => {
  const r = await render([issue(839, 'needs-owner')])
  try {
    element(r.target, '.row').click()
    await settle()
    assert.equal(location.hash, '#room=r&issue=839')
    history.replaceState(null, '', '/')
    window.dispatchEvent(new window.PopStateEvent('popstate'))
    await settle()
    assert.equal(r.target.querySelector('.dscroll'), null)
    history.replaceState(null, '', '#room=r&issue=839')
    window.dispatchEvent(new window.PopStateEvent('popstate'))
    await settle()
    assert.match(element(r.target, '.dscroll').textContent, /Issue 839/)
    history.replaceState(null, '', '#room=r&issue=900')
    window.dispatchEvent(new window.HashChangeEvent('hashchange'))
    await settle()
    assert.match(element(r.target, '.dscroll').textContent, /Issue 900/)
  } finally { await r.done() }
})

test('an issue link opens even when the list filter excludes it', async () => {
  const r = await render([issue(839, 'needs-owner')], { link: { roomId: 'r', issue: 900 } })
  try {
    assert.match(r.target.querySelector('.dscroll')?.textContent ?? '', /#900/)
  } finally { await r.done() }
})


test('a failed issue deep link shows its error and a way back to the list', async () => {
  Object.assign(globalThis, { issueViewFails: true })
  const r = await render([issue(839, 'needs-owner')], { link: { roomId: 'r', issue: 900 } })
  try {
    assert.match(r.target.querySelector('.dscroll')?.textContent ?? '', /Issue not found/)
    need(elements(r.target, '.dscroll button').find((b) => b.textContent.trim() === 'Issues')).click()
    await settle()
    assert.ok(r.target.querySelector('.list'))
    assert.equal(r.target.querySelector('.dscroll'), null)
  } finally { await r.done() }
})


test('opening Issues from a previous PR address makes Back return to the Issues list', async () => {
  const r = await render([issue(839, 'needs-owner')])
  try {
    history.replaceState(null, '', '#room=r&pr=800')
    element(r.target, '.row').click()
    await settle()
    const popped = new Promise((resolve) => window.addEventListener('popstate', resolve, { once: true }))
    history.back()
    await popped
    await settle()
    assert.equal(location.hash, '')
    assert.ok(r.target.querySelector('.list'))
    assert.equal(r.target.querySelector('.dscroll'), null)
  } finally { await r.done() }
})


test('Issues never loads PR merge status, on mount or focus', async () => {
  const r = await render([issue(839, 'needs-owner')])
  try {
    assert.equal(Reflect.get(globalThis, 'issueMergeCalls'), 0)
    window.dispatchEvent(new window.Event('focus'))
    await settle()
    assert.equal(Reflect.get(globalThis, 'issueMergeCalls'), 0)
  } finally { await r.done() }
})

const localIssue: RoomIssue = { id: 'msg_local', number: 12, title: 'Local issue', state: 'open', file: 'web/Example.svelte', line: 12, filer: 'builder', filedAt: 100 }

test('issue work uses local goal and PR progress plus GitHub work facts', () => {
  const active = { ...localIssue, state: 'goal', goalId: 'g1', inProgress: true, goalHolder: 'ana', fixPr: 42 }
  const activeRow = need(unifiedIssues([], [active], 'dyna', 'open', '')[0])
  assert.equal(activeRow.state, 'OPEN')
  assert.equal(issueWork(activeRow), 'in-progress')
  const finished = { ...active, inProgress: false, fixPr: undefined }
  assert.equal(issueWork(need(unifiedIssues([], [finished], 'dyna', 'open', '')[0])), 'unhandled')
  assert.equal(issueWork(need(unifiedIssues([], [{ ...localIssue, state: 'closed' }], 'dyna', 'all', '')[0])), 'other')
  assert.equal(issueWork(issue(1, 'working')), 'in-progress')
  assert.equal(issueWork(issue(2, 'has-pr')), 'in-progress')
  assert.equal(issueWork(issue(3, 'needs-owner')), 'unhandled')
})

test('Issues default to Unhandled and In progress names the goal holder and PR', async () => {
  const active = { ...localIssue, state: 'goal', goalId: 'g1', inProgress: true, goalHolder: 'ana', fixPr: 42 }
  const finished = { ...localIssue, id: 'msg_finished', number: 13, title: 'Finished goal', state: 'goal', goalId: 'g2', inProgress: false }
  const r = await render([], { localIssues: [active, finished] }, microtasks)
  try {
    assert.match(r.target.textContent || '', /Finished goal/)
    assert.doesNotMatch(r.target.textContent || '', /Local issue/)
    need(elements(r.target, 'button').find((b) => b.textContent?.trim() === 'In progress')).click()
    await microtasks()
    assert.match(r.target.textContent || '', /Local issue/)
    assert.match(r.target.textContent || '', /Held by ana/)
    assert.match(r.target.textContent || '', /Fix in #42/)
    assert.doesNotMatch(r.target.textContent || '', /Finished goal/)
  } finally { await r.done() }
})

test('unified issues deduplicate exports by repository URL and preserve local rows without GitHub', () => {
  const remote = issue(20, 'needs-owner')
  const exported = { ...localIssue, id: 'export', state: 'exported', githubUrl: remote.url + '/' }
  const rows = unifiedIssues([remote, issue(2, '')], [localIssue, exported], 'all', 'all', '')
  assert.deepEqual(rows.map(i => i.number || i.local?.id), [20, 'msg_local', 2])
  assert.equal(rows[0]?.local?.id, 'export')
  assert.equal(unifiedIssues([], [localIssue, { ...localIssue, state: 'closed' }], 'all', 'all', '').length, 1)
  assert.equal(unifiedIssues([], [localIssue, exported], 'dyna', 'all', '').length, 2)
  assert.deepEqual(unifiedIssues([], [localIssue], 'dyna', 'open', 'example.svelte').map(i => i.local?.id), ['msg_local'])
})

test('local issue search includes its room number', () => {
  const local = [localIssue, { ...localIssue, id: 'other', number: 13 }]
  assert.deepEqual(unifiedIssues([], local, 'dyna', 'all', 'd-12').map(i => i.local?.id), ['msg_local'])
  assert.deepEqual(unifiedIssues([], local, 'dyna', 'all', 'd12').map(i => i.local?.id), ['msg_local'])
  assert.deepEqual(unifiedIssues([], local, 'dyna', 'all', '12').map(i => i.local?.id), ['msg_local'])
  assert.deepEqual(unifiedIssues([], local, 'dyna', 'all', 'D-14'), [])
  // A number query compares the number, not a substring of another label.
  assert.deepEqual(unifiedIssues([], [...local, { ...localIssue, id: 'hundred', number: 112 }], 'dyna', 'all', '12').map(i => i.local?.id), ['msg_local'])
  assert.deepEqual(unifiedIssues([], [...local, { ...localIssue, id: 'hundred', number: 112 }], 'dyna', 'all', 'd1').map(i => i.local?.id), [])
  // A row without a number falls back to its message ID, like the Go label.
  assert.equal(roomIssueLabel({ ...localIssue, number: 0 }), 'msg_local')
})

test('issue references use room labels and keep GitHub numbers', () => {
  const rows = unifiedIssues([issue(839, 'needs-owner')], [localIssue], 'all', 'all', '')
  assert.deepEqual(issueReference(need(rows.find(i => i.local))), { kind: 'room_issue', number: 'D-12', title: localIssue.title, state: 'OPEN', url: '' })
  assert.deepEqual(issueReference(need(rows.find(i => i.number))), { kind: 'issue', number: 839, title: 'Issue 839', state: 'OPEN', url: 'https://github.com/o/r/issues/839' })
})

test('local issues remain readable and actionable when GitHub fails', async () => {
  Object.assign(globalThis, { issueListFails: true })
  const dismissed: RoomIssue[] = []
  const references: unknown[] = []
  const r = await render([], { localIssues: [localIssue], ondismississue: (i: RoomIssue) => dismissed.push(i), onmention: (reference: unknown) => references.push(reference) })
  try {
    assert.match(r.target.textContent || '', /Local issue/)
    assert.match(element(r.target, '.identity').textContent || '', /D-12/)
    assert.doesNotMatch(r.target.textContent || '', /msg_local/)
    need(elements(r.target, 'button').find((b) => b.getAttribute('aria-label') === 'Mention D-12 in chat')).click()
    assert.match(r.target.textContent || '', /No repository/)
    // Dismiss sits in the row menu, so the row stays one line.
    need(elements(r.target, 'button').find((b) => b.getAttribute('aria-label') === 'More actions for D-12')).click()
    await settle()
    need(elements(r.target, '[role=menu] button').find((b) => b.textContent?.trim() === 'Dismiss')).click()
    assert.equal(dismissed[0]?.id, localIssue.id)
    element(r.target, '.row').click()
    await settle()
    assert.equal(element(r.target, '.local-detail h2').textContent, 'D-12 Local issue')
    need(elements(r.target, '.local-detail button').find(b => b.textContent === 'Mention in chat')).click()
    assert.deepEqual(references.map(reference => (reference as { number: unknown }).number), ['D-12', 'D-12'])
    assert.match(element(r.target, '.local-detail').textContent || '', /Local body text/)
    assert.match(element(r.target, '.local-detail').textContent || '', /web\/Example.svelte:12/)
  } finally { await r.done() }
})


test('an issue row keeps one compact action group and puts the rest in its menu', async () => {
  const calls: string[] = []
  const r = await render([], {
    localIssues: [localIssue],
    onmention: () => {},
    onexportissue: () => calls.push('promote'),
    oncloseissue: () => calls.push('close'),
    ondismississue: () => calls.push('dismiss'),
  }, microtasks)
  try {
    const row = element(r.target, '.item')
    assert.equal(elements(row, 'button').filter((b) => b.textContent?.trim() === 'Mention in chat').length, 0, 'no wide button')
    const mention = need(elements(row, 'button').find((b) => b.getAttribute('aria-label') === 'Mention D-12 in chat'))
    assert.equal(mention.getAttribute('title'), 'Mention D-12 in chat', 'the icon has a tooltip')
    assert.equal(row.textContent?.includes('Promote'), false, 'the row does not show Promote')
    need(elements(row, 'button').find((b) => b.getAttribute('aria-label') === 'More actions for D-12')).click()
    await microtasks()
    assert.deepEqual(elements(r.target, '[role=menu] button').map((b) => b.textContent?.trim()), ['Promote', 'Close', 'Dismiss'])
    need(elements(r.target, '[role=menu] button').find((b) => b.textContent?.trim() === 'Promote')).click()
    assert.deepEqual(calls, ['promote'])
    await microtasks()
  } finally { await r.done() }
})

test('an unresolved export is history, not a closed GitHub issue', () => {
  const remote = issue(20, 'needs-owner')
  const exported = { ...localIssue, state: 'exported', githubUrl: remote.url }
  assert.deepEqual(unifiedIssues([], [exported], 'all', 'closed', ''), [])
  assert.deepEqual(unifiedIssues([], [exported], 'all', 'open', ''), [])
  const history = unifiedIssues([], [exported], 'all', 'all', '')
  assert.equal(history[0]?.state, 'UNKNOWN')
  assert.equal(history[0]?.url, remote.url)
  assert.equal(unifiedIssues([remote], [exported], 'all', 'open', '')[0]?.state, 'OPEN')
})
