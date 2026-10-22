import test from 'node:test'
import assert from 'node:assert/strict'
import { afterOpen } from './afterOpen.ts'

const flush = () => new Promise<void>((resolve) => queueMicrotask(resolve))
test('full view waits for its opening animation before loading the card', async () => {
  let ready = false
  const { promise: finished, resolve: finish } = Promise.withResolvers<void>()
  afterOpen({ getAnimations: () => [{ finished }] }, () => { ready = true })
  await flush()
  assert.equal(ready, false)
  finish()
  await flush()
  assert.equal(ready, true)
})
test('reduced motion loads the card without an animation event', async () => {
  let ready = false
  afterOpen({ getAnimations: () => [] }, () => { ready = true })
  await flush()
  assert.equal(ready, true)
})
test('closing during the opening animation does not load a card', async () => {
  let ready = false
  const { promise: finished, reject: cancel } = Promise.withResolvers<void>()
  const action = afterOpen({ getAnimations: () => [{ finished }] }, () => { ready = true })
  action.destroy()
  cancel(new Error('closed'))
  await flush()
  assert.equal(ready, false)
})
test('canceling the animation still releases an open card', async () => {
  let ready = false
  const { promise: finished, reject: cancel } = Promise.withResolvers<void>()
  afterOpen({ getAnimations: () => [{ finished }] }, () => { ready = true })
  cancel(new Error('motion preference changed'))
  await flush()
  assert.equal(ready, true)
})
