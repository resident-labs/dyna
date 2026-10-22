import test from 'node:test'
import assert from 'node:assert/strict'
import { prTracks } from './pr.ts'
import { isReady, statusBox } from './prlist.ts'
import type { ChangeView } from '../api/change.ts'

// The fields this file reads. Local changes set kind, and each variant sets only its own CI state.
type Change = Pick<ChangeView, 'state' | 'kind' | 'ci' | 'reviewState' | 'verdict' | 'head' | 'reviewHead'>
type Variant = Pick<ChangeView, 'ci' | 'kind'>

test('source approval cannot replace GitHub CI for merge readiness', () => {
  const variants: Variant[] = [{ ci: 'absent' }, { kind: 'local' }, { kind: 'local', ci: 'passed' }]
  for (const variant of variants) {
    const change: Change ={ state: 'open', reviewState: 'approved', verdict: 'approve', head: 'a', reviewHead: 'a', ...variant }
    assert.equal(prTracks(change).ready, false)
    assert.equal(isReady(change), false)
    const box = statusBox(change)
    assert.doesNotMatch(box.say, /reviewer runs local checks/)
    if (variant.ci !== 'passed') {
      const [ci] = box.steps
      assert.ok(ci !== undefined)
      assert.notEqual(ci.state, 'ok')
    }
  }
})
