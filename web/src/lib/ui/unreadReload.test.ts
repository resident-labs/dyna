import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { reloadsUnread } from './unreadReload.ts'

test('a members event that carried its row does not reload unread counts', () => {
  assert.equal(reloadsUnread({ kind: 'members', roomId: 'r1' }, 'r1', true), false)
})

test('new posts and whole-list member changes reload unread counts', () => {
  assert.equal(reloadsUnread({ kind: 'posts', roomId: 'r1' }, 'r1', false), true)
  assert.equal(reloadsUnread({ kind: 'members', roomId: 'r1' }, 'r1', false), true)
})

test('events for another room or of other kinds do not reload unread counts', () => {
  assert.equal(reloadsUnread({ kind: 'posts', roomId: 'r2' }, 'r1', false), false)
  assert.equal(reloadsUnread({ kind: 'posts', roomId: 'r1' }, '', false), false)
  assert.equal(reloadsUnread({ kind: 'jobs', roomId: 'r1' }, 'r1', false), false)
})

// The app asks reloadsUnread before every unread reload an event triggers.
test('App.svelte reloads unread counts for an event only through reloadsUnread', () => {
  const app = readFileSync(fileURLToPath(new URL('../../App.svelte', import.meta.url)), 'utf8')
  const reloads = app.split('\n').filter((line) => line.includes("soonRooms('feed')") && /\bev\b/.test(line))
  assert.ok(reloads.length > 0, 'no event reloads unread counts')
  for (const line of reloads) assert.match(line, /reloadsUnread\(ev, roomId, item\)/, line.trim())
})
