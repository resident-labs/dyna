import test from 'node:test'
import assert from 'node:assert/strict'
import { codeLike, splitPost } from './dataBlocks.ts'
import { scriptLabel, scriptSender } from './scriptSender.ts'

test('splitPost reads a fenced block, a bare line and a labelled line as data, and keeps prose', () => {
  const fenced = splitPost('Land it\n\n```json\n{\n  "unit": "CH"\n}\n```\nThanks.')
  assert.deepEqual(fenced, [
    { kind: 'text', text: 'Land it' },
    { kind: 'data', value: { unit: 'CH' } },
    { kind: 'text', text: 'Thanks.' },
  ])
  const old = splitPost('LANDING REQUEST after review: {"unit":"CH","list":[1]}\nRun it.')
  assert.deepEqual(old, [
    { kind: 'text', text: 'LANDING REQUEST after review:' },
    { kind: 'data', value: { unit: 'CH', list: [1] } },
    { kind: 'text', text: 'Run it.' },
  ])
  // Braces in prose, empty objects and a non-JSON fence are not data.
  const prose = 'Use {braces} and {} here.\n```json\n"just text"\n```'
  assert.deepEqual(splitPost(prose), [{ kind: 'text', text: prose }])
})

test('codeLike picks paths and hashes only', () => {
  assert.equal(codeLike('/home/ari/.cache/x.json'), true)
  assert.equal(codeLike('31cdb141c7d5f65c978a282ebb2a5ff71eeeb41d'), true)
  assert.equal(codeLike('swarm/continuous-wave5'), true)
  assert.equal(codeLike('independent sol exact-head approval'), false)
})

test('a script sender comes from the stored run, or from an older post address', () => {
  assert.equal(scriptLabel({ runId: 'wf_20261026-120344-604cf9b5', workflow: 'landing' }), 'landing')
  const old = scriptSender({ from: 'run:wf_20261026-120344-604cf9b5' })
  assert.equal(old && scriptLabel(old), 'Workflow 604cf9b5')
  assert.equal(scriptSender({ from: 'horizon:integrator' }), undefined)
})
