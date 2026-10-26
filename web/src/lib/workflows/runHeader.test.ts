import test from 'node:test'
import assert from 'node:assert/strict'
import { runLine, spanText, startedTitle } from './runHeader.ts'

const minute = 60_000
const hour = 60 * minute
const iso = (ago: number) => new Date(Date.now() - ago).toISOString()

test('spans read at a glance size', () => {
  assert.equal(spanText(45_000), '45s')
  assert.equal(spanText(48 * minute + 59_000), '48m')
  assert.equal(spanText(2 * hour + 5 * minute), '2h 05m')
})

test('a running run says how long it has run and how many agents work, and failed only above zero', () => {
  const start = iso(48 * minute + 59_000)
  const base = { status: 'running', startedAt: start, working: 5, failed: 0, agents: 33 }
  assert.equal(runLine(base, Date.now()), 'Running for 48m · 5 working · 33 agents')
  assert.equal(runLine({ ...base, failed: 2 }, Date.now()), 'Running for 48m · 5 working · 33 agents · 2 failed')
  assert.equal(runLine({ ...base, working: 1, agents: 1 }, Date.now()), 'Running for 48m · 1 working · 1 agent')
  assert.equal(runLine({ ...base, agents: 0, working: 0 }, Date.now()), 'Running for 48m', 'a run with no agents yet shows no counts')
})

test('a run that ended says when and how long it took', () => {
  const run = { status: 'ok', startedAt: iso(2 * hour + 48 * minute), endedAt: iso(2 * hour), working: 0, failed: 0, agents: 33 }
  assert.equal(runLine(run, Date.now()), 'Finished 2h ago · took 48m')
  assert.equal(runLine({ ...run, status: 'error', failed: 1 }, Date.now()), 'Failed 2h ago · took 48m · 1 failed')
  assert.equal(runLine({ ...run, status: 'canceled' }, Date.now()), 'Canceled 2h ago · took 48m')
  assert.equal(runLine({ ...run, endedAt: undefined, status: 'unknown' }, Date.now()), 'Started 2h ago', 'no end recorded: say only when it started')
})

test('the tooltip names the exact start time', () => {
  assert.match(startedTitle('2026-10-26T19:34:05Z'), /^Started .*2026.*/)
  assert.equal(startedTitle('not a time'), '')
})
