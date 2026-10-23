import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { stripTypes, topLevelFunctions } from '../../../test/svelte-source.ts'
import { installRouterShim } from '../../../test/appRouterShim.ts'
import { installSessionView } from '../../../test/appModules.ts'
import { member, session } from '../../../test/session-fixtures.ts'

// A completion link for the session that is already open skips openFresh, so
// the transcript it brings must replace the error of the read that failed.
const source = readFileSync(new URL('../../App.svelte', import.meta.url), 'utf8')
const openSession = topLevelFunctions(source, ['openSession']).get('openSession')
if (!openSession) throw new Error('App.svelte has no openSession')
const openCode: string = openSession

function app(failed: boolean) {
  const c: Record<string, unknown> = {
    drill: { id: 'ses_one', name: 'one' }, sessionTurn: 3, sessionTab: 'computer', selectedThread: 'thread',
    session: null, sessionError: failed ? 'Could not load the transcript' : '', sessionVisit: 1,
    card: null, showPins: true, narrow: false, showMembers: true, sessionView: null,
    clearItemLink() {}, setTimeout() {}, loadSession() { throw new Error('the link brings its transcript') },
  }
  vm.createContext(c)
  installRouterShim(c)
  installSessionView(c)
  vm.runInContext(stripTypes(openCode), c)
  return c
}

test('the transcript a completion link brings clears the error of a read that failed', () => {
  const c = app(true)
  const brought = session('ses_one', { member: member('ses_one', { name: 'one' }) })
  ;(c.openSession as (member: { id: string; name: string }, turn: number, initial: unknown) => void)({ id: 'ses_one', name: 'one' }, 3, brought)
  assert.equal(c.session, brought)
  assert.equal(c.sessionError, '')
})
