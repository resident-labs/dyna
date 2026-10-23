import test from 'node:test'
import assert from 'node:assert/strict'
import { TOAST_ACTION_MS, TOAST_MS, clipText, dismissDelay, makeToast, toneRole } from './toast.ts'

test('a plain result needs only a subject', () => {
  const t = makeToast({ subject: 'Copied the address' })
  assert.deepEqual(t, { tone: 'info', subject: 'Copied the address', text: '', action: undefined, dismiss: true })
})

test('a subject reads as a heading, without a closing period', () => {
  assert.equal(makeToast({ subject: 'Renamed to #dyna.' }).subject, 'Renamed to #dyna')
  assert.equal(makeToast({ subject: 'Saved' }).subject, 'Saved')
})

test('a result keeps its tone, sentence and action', () => {
  const run = () => {}
  const t = makeToast({ tone: 'success', subject: 'Dyna watches #292', text: 'Its updates arrive here.', action: { label: 'Open', run } })
  assert.equal(t.tone, 'success')
  assert.equal(t.text, 'Its updates arrive here.')
  assert.equal(t.action?.label, 'Open')
  assert.equal(t.action?.run, run)
})

test('a failure interrupts a screen reader and a result waits', () => {
  assert.equal(toneRole('error'), 'alert')
  assert.equal(toneRole('success'), 'status')
  assert.equal(toneRole('info'), 'status')
})

test('a toast with an action stays longer than a plain one', () => {
  assert.equal(dismissDelay(makeToast({ subject: 'Saved' })), TOAST_MS)
  assert.equal(dismissDelay(makeToast({ subject: 'Saved', action: { label: 'Undo', run: () => {} } })), TOAST_ACTION_MS)
})

test('long text clips at a word boundary instead of a raw value', () => {
  assert.equal(clipText('Done'), 'Done')
  assert.equal(clipText('set up the nightly build and the merge queue', 24), 'set up the nightly…')
  assert.equal(clipText('the queue is stuck', 6), 'the qu…')
  assert.equal(clipText('one\n  two   three'), 'one two three')
})
