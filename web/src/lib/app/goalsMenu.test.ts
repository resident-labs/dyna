import test from 'node:test'
import assert from 'node:assert/strict'
import { installGoals } from '../../../test/appModules.ts'
import { expedite, none } from '../rooms/expedite.ts'
import type { Member } from '../api/room.ts'
import type { Goal } from '../api/goal.ts'
import { member } from '../../../test/session-fixtures.ts'

// The session menu's expedite item follows the expedited goal the page passes
// as the menu is drawn. A goal that expires while the menu is open clears the
// scope without changing the goals, so the item must stop offering "Stop".
type MenuSlice = { label: string }

function page(goals: Goal[]): (expeditedId: string) => MenuSlice[] {
  const lead = member('ses_1', { name: 'lead', address: 'repo:lead' })
  const state: Record<string, unknown> = {
    roomId: 'room_20261017-100000-aaaaaaaa',
    hierarchy: [lead],
    goals,
    // The module's knownGoals is a $derived, so the flat state supplies it.
    knownGoals: goals,
    fetchedGoal: null,
  }
  installGoals(state)
  const expediteItems = state.expediteItems as (m: Member, expeditedId: string) => MenuSlice[]
  return (expeditedId: string) => expediteItems(lead, expeditedId)
}

const held: Goal[] = [{ id: 'g1', state: 'active', holders: ['ses_1'] } as Goal]

test('the expedite item follows the expedited goal the page passes', () => {
  const items = page(held)
  expedite.set({ ...none, goalId: 'g1' })
  // The scope still names g1, but the page passes '': the goal expired, so the
  // menu offers Expedite again instead of a Stop that would restart it.
  assert.equal(items('')[0]?.label, 'Expedite g1')
  assert.equal(items('g1')[0]?.label, 'Stop expediting')
  expedite.set(none)
})
