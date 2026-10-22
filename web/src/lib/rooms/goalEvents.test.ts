import test from 'node:test'
import assert from 'node:assert/strict'
import { createGoalEvents, type EventsState, type GoalEvent } from './goalEvents.ts'

interface Page {
  items: GoalEvent[]
  older: boolean
  cursor: string
}

const page = (texts: string[], older: boolean, cursor: string): Page => ({
  items: texts.map((text, i) => ({ id: `e${i}`, goal: 'g1', by: 'lead', event: 'note', text, at: '2026-10-21T18:00:00Z' })),
  older,
  cursor,
})

test('the events journal loads newest first, pages older with the cursor, and drops a late answer for another goal', async () => {
  const calls: unknown[][] = []
  const answers: ((p: Page) => void)[] = []
  const api = {
    goalEvents: (room: string, goal: string, before: string, limit: number) => {
      calls.push([room, goal, before, limit])
      return new Promise<Page>((resolve) => answers.push(resolve))
    },
  }
  let state: EventsState = { items: [], older: false, cursor: '', loading: false, error: '' }
  const loader = createGoalEvents(api, (s) => (state = s))
  const tick = () => new Promise((r) => setTimeout(r, 0))
  // answer shifts the queued resolver for the oldest pending call.
  const answer = (p: Page) => {
    const resolve = answers.shift()
    if (!resolve) throw new Error('no answer queued')
    resolve(p)
  }
  loader.open('room_a', 'g1')
  assert.equal(state.loading, true)
  answer(page(['third', 'second'], true, 'e1'))
  await tick()
  assert.deepEqual(state.items.map((i) => i.text), ['third', 'second'])
  assert.equal(state.older, true)
  loader.older()
  const secondCall = calls.at(1)
  assert.deepEqual(secondCall, ['room_a', 'g1', 'e1', 20])
  answer(page(['first'], false, ''))
  await tick()
  assert.deepEqual(state.items.map((i) => i.text), ['third', 'second', 'first'])
  assert.equal(state.older, false)
  loader.open('room_a', 'g2')
  loader.open('room_a', 'g1')
  answer(page(['stale'], false, ''))
  answer(page(['fresh'], false, ''))
  await tick()
  assert.deepEqual(state.items.map((i) => i.text), ['fresh'], 'the answer for the earlier goal is dropped')
})
