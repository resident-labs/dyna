import test from 'node:test'
import assert from 'node:assert/strict'
import { historySegments } from './nodeHistory.ts'

test('history leaves gaps for missed probes and missing readings', () => {
  const now = Date.parse('2026-10-30T12:00:00Z')
  const at = (minutes: number) => new Date(now + minutes * 60_000).toISOString()
  const lines = historySegments([
    { at: at(-30), cpuPercent: 40, memoryPercent: 50 },
    { at: at(-25), cpuPercent: 50, memoryPercent: null },
    { at: at(-5), cpuPercent: 60, memoryPercent: 60 },
  ], now)
  assert.equal(lines.length, 1)
  assert.notEqual(lines[0]?.cpu, '')
  assert.equal(lines[0]?.memory, '')
})
