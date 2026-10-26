import test from 'node:test'
import assert from 'node:assert/strict'
import { byUrgency, cellOf, laneGrid, otherStage, type CellState, type PhaseInput } from './lanes.ts'

const ph = (phase: string, state: CellState = 'ok', extra: Partial<PhaseInput> = {}): PhaseInput => ({ phase, state, done: state === 'running' ? 0 : 1, total: 1, ...extra })

test('lanes and stages come from names, with repeated stages counted in one cell', () => {
  const grid = laneGrid([ph('CH prepare'), ph('BS prepare'), ph('BS review'), ph('BS fix'), ph('BS review'), ph('CH review', 'running')])
  assert.ok(grid)
  assert.deepEqual(grid.stages, ['prepare', 'review', 'fix'])
  assert.deepEqual(grid.lanes.map((l) => l.name), ['CH', 'BS'])
  const [ch, bs] = grid.lanes
  assert.ok(ch && bs)
  assert.equal(bs.cells[1]?.runs, 2)
  assert.deepEqual(bs.cells[1]?.phases, [2, 4])
  assert.equal(bs.cells[1]?.total, 2)
  assert.equal(bs.cells[2]?.runs, 1)
  assert.equal(ch.cells[2], null)
  assert.equal(ch.state, 'running')
})

test('phases without shared stages get no grid', () => {
  assert.equal(laneGrid([ph('Plan'), ph('Build the thing'), ph('Report')]), null)
  assert.equal(laneGrid([ph('CH prepare'), ph('BS build')]), null, 'no stage repeats across lanes')
  assert.equal(laneGrid([]), null)
})

test('a phase that does not split lands in the unnamed stage when most phases split', () => {
  const grid = laneGrid([ph('A prepare'), ph('B prepare'), ph('A build'), ph('Summary')])
  assert.ok(grid)
  assert.deepEqual(grid.stages, ['prepare', 'build', otherStage])
  assert.equal(grid.lanes[2]?.name, 'Summary')
  assert.equal(laneGrid([ph('A prepare'), ph('B prepare'), ph('Summary'), ph('Report')]), null, 'under 70% split')
})

test('an explicit lane and stage win over the name', () => {
  const grid = laneGrid([ph('first thing', 'ok', { lane: 'api', stage: 'build' }), ph('second', 'ok', { lane: 'web', stage: 'build' })])
  assert.ok(grid)
  assert.deepEqual(grid.stages, ['build'])
  assert.deepEqual(grid.lanes.map((l) => l.name), ['api', 'web'])
})

test('cellOf finds the cell that holds a phase group', () => {
  const grid = laneGrid([ph('A prepare'), ph('B prepare'), ph('B review'), ph('B review')])
  assert.ok(grid)
  assert.deepEqual(cellOf(grid, 3), { lane: 'B', col: 1 })
  assert.equal(cellOf(grid, 9), null)
})

test('a failed cell makes its lane failed, and urgent lanes sort first', () => {
  const grid = laneGrid([ph('A prepare'), ph('B prepare', 'running'), ph('C prepare', 'error'), ph('C build')])
  assert.ok(grid)
  assert.deepEqual(byUrgency(grid.lanes).map((l) => l.name), ['C', 'B', 'A'])
})

test('commands count into their lane cell beside the agents', () => {
  const grid = laneGrid([ph('CH prepare', 'ok', { commands: 1 }), ph('BS prepare'), ph('CH prepare', 'running', { commands: 2 }), ph('CH review')])
  assert.ok(grid)
  const [ch, bs] = grid.lanes
  assert.ok(ch && bs)
  assert.equal(ch.cells[0]?.commands, 3, 'commands sum over rounds')
  assert.equal(ch.cells[0]?.total, 2, 'agents still count in total')
  assert.equal(ch.cells[0]?.runs, 2)
  assert.equal(bs.cells[0]?.commands, 0)
})
