// An isolated component fixture. It never contacts the engine.
import { mount, tick } from 'svelte'
import PRCard from '../src/lib/prs/PRCard.svelte'
import { api } from '../src/api.js'
import type { Row } from '../src/lib/prs/prlist.ts'
import { need } from '../test/need.ts'

const summary = '## Findings\n\nFix `parser.go:42`.\n\n' + 'Review detail.\n\n'.repeat(150) + 'Tail: `tail.go:99`.'
const pr: Row = { key: 'repo#17', changeId: 'c17', roomId: 'fixture', number: 17, title: 'Parser', repo: 'owner/repo', state: 'open', status: 'changes-requested', ci: 'passed', verdict: 'changes', reviewState: 'changes', hasReviewSummary: true, detailVersion: 'v1', reviewPublishing: 'local', isDraft: false, created: '', checked: '', queuePosition: null, botReviews: null, checks: null }
const props = $state({ pr })
// A detail request the card has made; the test answers it.
const requests: { room: string; key: string; resolve: (detail: object) => void }[] = []
api.changeDetail = (room: string, key: string) => new Promise((resolve) => requests.push({ room, key, resolve }))
mount(PRCard, { target: need(document.getElementById('app'), 'app'), props })
const findButton = (text: string) => [...document.querySelectorAll('button')].find((node) => node.textContent?.trim() === text)
const button = (text: string) => need(findButton(text), `button ${text}`)
const waitFor = <T>(read: () => T | null | undefined): Promise<T> => {
  const value = read()
  if (value) return Promise.resolve(value)
  return new Promise((resolve) => {
    const observer = new MutationObserver(() => {
      const value = read()
      if (value) { observer.disconnect(); resolve(value) }
    })
    observer.observe(document.body, { subtree: true, childList: true, attributes: true, characterData: true })
  })
}
const detail = () => ({ ...props.pr, summary, findings: [{ id: 'f1', severity: 'should-fix', why: 'Keep the reply.' }], reviewRounds: [] })

if (new URLSearchParams(location.search).get('screen') === 'test') {
  const results: { name: string; pass: boolean }[] = []
  const check = (name: string, pass: unknown) => { results.push({ name, pass: !!pass }); if (!pass) throw new Error(name) }
  try {
    await tick()
    check('unopened cards do not request detail', requests.length === 0)
    button('Read the review').click()
    await tick()
    check('opening the real card requests its detail', requests.length === 1 && requests[0]?.key === 'repo#17' && requests[0].room === 'fixture')
    need(requests[0]).resolve(detail())
    await waitFor(() => findButton('Question'))
    check('full Markdown renders through the detail API', document.querySelector('h2')?.textContent === 'Findings' && document.body.textContent?.includes('parser.go:42') && document.body.textContent?.includes('tail.go:99') && (document.body.textContent?.match(/Review detail\./g) || []).length === 150)
    check('local publishing label remains visible', document.body.textContent?.includes('Dyna only') && !document.body.textContent?.includes('Published on GitHub'))
    button('Question').click()
    const input = await waitFor(() => document.querySelector('textarea'))
    input.value = 'Please keep this reply.'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    props.pr = { ...props.pr, checked: 'later' }
    await tick()
    check('row replacement keeps the typed reply without refetching', document.querySelector('textarea') === input && input.value === 'Please keep this reply.' && requests.length === 1)
    props.pr = { ...props.pr, detailVersion: 'v2' }
    await tick()
    check('detail refresh keeps the reply while loading', requests.length === 2 && document.querySelector('textarea') === input && input.value === 'Please keep this reply.')
    need(requests[1]).resolve(detail())
    await tick()
    check('new detail keeps the same reply input', document.querySelector('textarea') === input && input.value === 'Please keep this reply.')
    props.pr = { ...props.pr, reviewUrl: 'https://github.com/owner/repo/pull/17#pullrequestreview-88' }
    await tick()
    check('historical public feedback keeps its label', document.body.textContent?.includes('Published on GitHub') && !document.body.textContent?.includes('Dyna only'))
    props.pr = { ...props.pr, key: 'repo#18', changeId: 'c18' }
    await tick()
    check('another change clears the previous reply', !document.querySelector('textarea') && requests.length === 3)
    need(requests[2]).resolve(detail())
    await waitFor(() => findButton('Question'))
    button('Question').click()
    await tick()
    check('another change starts with an empty reply', document.querySelector('textarea')?.value === '')
  } catch (error) {
    results.push({ name: error instanceof Error ? error.message : String(error), pass: false })
  }
  const output = document.createElement('pre')
  output.id = 'results'
  output.textContent = JSON.stringify(results)
  document.body.append(output)
  document.body.dataset.failed = String(results.some((result) => !result.pass))
  document.body.dataset.done = 'true'
}
