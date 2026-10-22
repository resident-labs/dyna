import test from 'node:test'
import assert from 'node:assert/strict'
import type { Facts } from './pr.ts'
import { prHash, parsePRHash, findPR, lookupNumber, issueHash, parseIssueHash, fileHash, parseFileHash } from './prlink.ts'

test('a PR link names the room, the PR and a tab other than the conversation', () => {
  assert.equal(prHash('room_1', 741, 'checks'), '#room=room_1&pr=741&tab=checks')
  assert.equal(prHash('room_1', 741), '#room=room_1&pr=741')
  assert.equal(prHash('room_1', 'local:abc', 'conversation'), '#room=room_1&pr=local%3Aabc')
})

test('a PR link reads back, with an unknown tab as the conversation', () => {
  assert.deepEqual(parsePRHash('#room=room_1&pr=741&tab=files'), { roomId: 'room_1', pr: '741', tab: 'files' })
  assert.deepEqual(parsePRHash('#room=room_1&pr=741&tab=nope'), { roomId: 'room_1', pr: '741', tab: 'conversation' })
  assert.deepEqual(parsePRHash('#room=room_1&pr=741&tab=review'), { roomId: 'room_1', pr: '741', tab: 'conversation' })
  assert.equal(parsePRHash('#room=room_1&post=msg_1'), null)
  assert.equal(parsePRHash(''), null)
})

test('a link finds a GitHub PR by number and a local change by its id', () => {
  const rows: Facts[] = [{ changeId: 'a', kind: 'pr', number: 741 }, { changeId: 'local:abc', kind: 'local' }]
  assert.equal(findPR(rows, '741')?.changeId, 'a')
  assert.equal(findPR(rows, 'local:abc')?.changeId, 'local:abc')
  assert.equal(findPR(rows, '9'), null)
  assert.equal(findPR(null, '741'), null)
})

test('a linked GitHub PR outside the loaded list is looked up by its number', () => {
  const rows: Facts[] = [{ changeId: 'a', kind: 'pr', number: 741 }]
  assert.equal(lookupNumber(rows, { pr: '9' }), 9)
  assert.equal(lookupNumber(rows, { pr: '741' }), 0)
  assert.equal(lookupNumber(rows, { pr: 'local:abc' }), 0)
  assert.equal(lookupNumber(rows, null), 0)
})

test('the commits tab has a deep link', () => {
  assert.equal(prHash('room_1', 741, 'commits'), '#room=room_1&pr=741&tab=commits')
  assert.equal(parsePRHash('#room=room_1&pr=741&tab=commits')?.tab, 'commits')
})


test('issue links keep the room and positive issue number, without a PR tab', () => {
  assert.equal(issueHash('room_1', 839), '#room=room_1&issue=839')
  assert.deepEqual(parseIssueHash('#room=room_1&issue=839'), { roomId: 'room_1', issue: 839 })
  for (const hash of ['', '#room=r&pr=839', '#issue=-1', '#issue=1.5', '#issue=oops', '#issue=9007199254740992']) assert.equal(parseIssueHash(hash), null)
})

test('a file link names the room, the file path and an old commit', () => {
  assert.equal(fileHash('room_1', 'src/a b.js'), '#room=room_1&file=src%2Fa+b.js')
  assert.equal(fileHash('room_1', 'main.js', 'abc123'), '#room=room_1&file=main.js&at=abc123')
  assert.deepEqual(parseFileHash('#room=room_1&file=src%2Fa+b.js&at=abc123'), { roomId: 'room_1', file: 'src/a b.js', at: 'abc123', was: '' })
  assert.deepEqual(parseFileHash('#room=room_1&file=main.js'), { roomId: 'room_1', file: 'main.js', at: '', was: '' })
  assert.equal(fileHash('room_1', 'new.txt', 'abc123', 'old.txt'), '#room=room_1&file=new.txt&at=abc123&was=old.txt')
  assert.equal(parseFileHash('#room=room_1&file=new.txt&at=abc123&was=old.txt')?.was, 'old.txt')
  for (const hash of ['', '#room=room_1', '#room=room_1&pr=7', '#room=room_1&file=']) assert.equal(parseFileHash(hash), null)
})
