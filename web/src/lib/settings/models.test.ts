import assert from 'node:assert/strict'
import test from 'node:test'
import { SERVICE_KEYS, REVIEWER_KEY, serviceTitles, reviewerItem } from './models.ts'
import type { ServiceView } from '../api/service.ts'

const reviewer = (over: Partial<ServiceView> = {}): ServiceView => ({ role: 'reviewer', name: 'PR reviewers', page: '', profile: 'sol', pinned: '', source: '', reason: '', waiting: false, scope: 'room', sessions: 0, profiles: ['sol', 'opus'], ...over })

test('the Services group holds the reviewers, the librarian and Voice, each with a title', () => {
  assert.deepEqual([...SERVICE_KEYS], [REVIEWER_KEY, 'library.profile', 'voice.provider'])
  assert.ok(SERVICE_KEYS.every((k) => serviceTitles[k]))
})

test('the reviewer row is an Auto choice plus the profiles the service can run', () => {
  const item = reviewerItem([reviewer({ pinned: 'opus' })], 'a')
  assert.deepEqual(item?.options, ['', 'sol', 'opus'])
  assert.equal(item?.optionLabels?.[0], 'Auto')
  assert.equal(item?.value, 'opus')
})

test('a pin the service cannot run stays visible as not enabled', () => {
  const item = reviewerItem([reviewer({ pinned: 'old' })], 'a')
  assert.equal(item?.options?.at(-1), 'old')
  assert.equal(item?.optionLabels?.at(-1), 'old (not enabled)')
})

test('no row without an open room or a reviewer service', () => {
  assert.equal(reviewerItem([reviewer()], ''), null)
  assert.equal(reviewerItem([], 'a'), null)
})
