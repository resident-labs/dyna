import test from 'node:test'
import assert from 'node:assert/strict'
import { mentionOptions, matchMentions, memberChoices } from './mentions.ts'

test('an internal root session has only one destination in live mention choices', () => {
  assert.deepEqual(memberChoices([{ name: 'root' }, { name: 'lead' }, { name: 'old', closed: true }]), ['@team', '@root', '@lead'])
})

test('mention choices carry the composer hints and match by prefix', () => {
  const options = mentionOptions(['@root', '@team', '@lead', '@parent'])
  assert.equal(options[0]?.hint, 'external orchestrators watching this room')
  assert.equal(options[1]?.hint, 'your top-level sessions')
  assert.equal(options[2]?.hint, 'member')
  assert.deepEqual(matchMentions(options, '@p').map((o) => o.value), ['@parent'])
  assert.deepEqual(matchMentions(options, '@').map((o) => o.value), ['@root', '@team', '@lead', '@parent'])
  assert.equal(matchMentions(options, '@', 2).length, 2)
})

test('file mentions browse folders and insert paths without creating recipients', async () => {
  const { fileMentionOptions, fileMentionDirectory, fileMentionText } = await import('./mentions.ts')
  const entries = [{ path: 'web/src', name: 'src', kind: 'dir' as const }, { path: 'web/package.json', name: 'package.json', kind: 'file' as const }] as const
  assert.equal(fileMentionDirectory('@web/pa'), 'web')
  assert.deepEqual(fileMentionOptions(entries, '@web/p').map(o => o.value), ['@web/package.json'])
  assert.equal(fileMentionText(entries[0]), '@web/src/')
  assert.equal(fileMentionText(entries[1]), '`web/package.json` ')
  assert.equal(fileMentionText({ path: 'odd`file.txt', name: 'odd`file.txt', kind: 'file' }), '``odd`file.txt`` ')
})
