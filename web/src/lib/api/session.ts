// A session's transcript and its queued, transferred and consolidated work.
// The JSON types are generated from Go by tygo; the guards below validate the
// close recovery before it controls any buttons.
import type {
  Action as CloseAction,
  Reason as CloseReason,
  Recovery as CloseRecovery,
  Responsibility,
  ResponsibilityKind,
} from './session.gen.ts'

export type * from './session.gen.ts'
export type {
  Action as CloseAction,
  Reason as CloseReason,
  Recovery as CloseRecovery,
  Profile as MemberProfile,
  ReferenceRow as SessionReference,
} from './session.gen.ts'

const isCloseReason = (value: unknown): value is CloseReason => value === 'responsibilities' || value === 'goal-needs-agent'
const isCloseAction = (value: unknown): value is CloseAction => value === 'retry' || value === 'force-close'
const isResponsibilityKind = (value: unknown): value is ResponsibilityKind => value === 'pr' || value === 'goal' || value === 'item' || value === 'question' || value === 'child' || value === 'shell' || value === 'workflow'
const isResponsibility = (value: unknown): value is Responsibility => {
  if (typeof value !== 'object' || value === null) return false
  return isResponsibilityKind(Reflect.get(value, 'kind')) && typeof Reflect.get(value, 'id') === 'string' && typeof Reflect.get(value, 'text') === 'string'
}

// asCloseRecovery validates a server recovery before it controls any buttons.
export function asCloseRecovery(value: unknown): CloseRecovery | null {
  if (typeof value !== 'object' || value === null) return null
  const reason: unknown = Reflect.get(value, 'reason')
  const actions: unknown = Reflect.get(value, 'actions')
  const items: unknown = Reflect.get(value, 'items')
  if (!isCloseReason(reason) || !Array.isArray(actions) || !actions.every(isCloseAction) || !Array.isArray(items) || !items.every(isResponsibility)) return null
  return { reason, actions, items }
}

// closeRecoveryOf reads both a thrown API error and a bulk result row.
export function closeRecoveryOf(value: unknown): CloseRecovery | null {
  if (typeof value !== 'object' || value === null) return null
  return asCloseRecovery(Reflect.get(value, 'close'))
}

export const allowsForceClose = (value: unknown): boolean => closeRecoveryOf(value)?.actions?.includes('force-close') ?? false
