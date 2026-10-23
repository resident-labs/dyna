import test from 'node:test'
import assert from 'node:assert/strict'
import { blockState, bytesOf, plainLog, commandOf, isLogChunk, mergeJob, metaOf, sizeOf } from './shell.ts'
import type { ShellJob } from './shell.ts'

const iso = (s: number): string => new Date(Date.UTC(2026, 9, 20, 10, 0, s)).toISOString()
const job = (extra: Partial<ShellJob> = {}): ShellJob => ({ id: 'sh_1', command: ['env', 'A=1', 'bash', '-c', 'go test ./...'], name: 'Run the tests', cwd: '/', state: 'exited', code: 0, started: iso(0), ended: iso(5), ...extra })

test('the state follows the job: running, background, done, failed, stopped', () => {
  assert.equal(blockState(job({ state: 'running', ended: undefined })), 'running')
  assert.equal(blockState(job({ state: 'starting', ended: undefined })), 'running')
  assert.equal(blockState(job({ state: 'running', ended: undefined, backgroundAt: iso(30) })), 'background')
  assert.equal(blockState(job()), 'done')
  assert.equal(blockState(job({ code: 1 })), 'failed')
  assert.equal(blockState(job({ state: 'lost', ended: undefined })), 'failed')
  assert.equal(blockState(job({ code: -1, signal: 'terminated', stopBy: 'agent' })), 'stopped')
  assert.equal(blockState(job({ code: -1, signal: 'killed' })), 'failed')
})

test('the meta names the outcome and duration, or who stopped the job', () => {
  assert.equal(metaOf(job(), 0), 'succeeded in 5s')
  assert.equal(metaOf(job({ code: 2 }), 0), 'failed with exit code 2 · 5s')
  assert.equal(metaOf(job({ backgroundAt: iso(30), ended: iso(200) }), 0), 'succeeded in the background · 3m 20s')
  assert.equal(metaOf(job({ code: -1, signal: 'terminated', stopBy: 'owner' }), 0), 'stopped by owner · terminated · 5s')
  const running = job({ state: 'running', ended: undefined })
  assert.equal(metaOf(running, Date.parse(iso(7))), '7s')
  assert.equal(metaOf({ ...running, backgroundAt: iso(30) }, Date.parse(iso(75))), 'in background · 1m 15s')
})

test('the live row lays over the snapshot but keeps a tail the row lacks', () => {
  const snapshot = job({ state: 'running', ended: undefined, tail: 'a\nb', outputBytes: 4 })
  assert.equal(mergeJob(snapshot, undefined), snapshot)
  assert.deepEqual(mergeJob(snapshot, { ...snapshot, tail: 'c' }).tail, 'c')
  const ended = mergeJob(snapshot, job({ outputLines: 2 }))
  assert.deepEqual([ended.state, ended.tail, ended.outputBytes], ['exited', 'a\nb', 4])
})

test('command, size and log chunk guards', () => {
  assert.equal(commandOf(job()), 'go test ./...')
  assert.equal(sizeOf(job({ outputLines: 1204 })), '1,204 lines')
  assert.equal(sizeOf(job({ outputLines: 1 })), '1 line')
  assert.equal(sizeOf(job({ state: 'running', outputBytes: 2048 })), '2 KB so far')
  assert.equal(bytesOf(80), '80 B')
  assert.equal(bytesOf(300 * 1024), '300 KB')
  assert.equal(isLogChunk({ text: '', next: 0, size: 0 }), true)
  assert.equal(isLogChunk({ text: 1, next: 0, size: 0 }), false)
  assert.equal(isLogChunk(null), false)
})

test('joined log text reads as a terminal would: no escape codes, CRLF is a line end, redraws keep the last', () => {
  assert.equal(plainLog('alpha\r\nbeta\r\n'), 'alpha\nbeta\n')
  assert.equal(plainLog('progress 10%\rprogress 100%\r\ndone'), 'progress 100%\ndone')
  assert.equal(plainLog('plain'), 'plain')
  const codes: [string, string][] = [
    ['\x1b[31;1mred\x1b[0m', 'red'],
    ['\x1b]0;title\x07ok', 'ok'],
    ['\x1b]0;' + 'x'.repeat(300) + '\x07ok', 'ok'],
    ['\x1b]8;;http://example.com/' + 'a'.repeat(300) + '\x1b\\link\x1b]8;;\x1b\\', 'link'],
    ['\x1b(Bhello', 'hello'],
    ['\x1bPq data\x1b\\after', 'after'],
    ['text\x1b[2K', 'text'],
    ['text\x1b', 'text'],
  ]
  for (const [raw, want] of codes) assert.equal(plainLog(raw), want, JSON.stringify(raw))
})

