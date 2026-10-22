// Whole session objects for tests, each with every field its type requires. A test names
// only the fields it cares about.
import type { Member, Post } from '../src/lib/api/room.ts'
import type { Session, Turn } from '../src/lib/api/session.ts'
import type { DmRow } from '../src/lib/sessions/dm.ts'

export const member = (id: string, extra: Partial<Member> = {}): Member => ({
  resumable: false, name: id, address: id, depth: 0, children: 0, status: 'idle', state: 'idle',
  turns: 0, created: '', lastActivity: '', hue: 0, id, ...extra,
})

export const post = (id: string, extra: Partial<Post> = {}): Post => ({
  wake: false, id, ts: 0, roomId: 'room', from: 'owner', to: null, text: '',
  owner: false, completion: false, fromHue: 0, speaker: '', ...extra,
})

export const turn = (n: number, extra: Partial<Turn> = {}): Turn => ({
  n, status: 'ok', started: '', ended: '', usage: {}, items: [], ...extra,
})

export const session = (id: string, extra: Partial<Session> = {}): Session => ({
  earlier: false, newer: false, member: member(id), address: '', posts: [], media: [], dir: '', usage: {}, turns: [], ...extra,
})

// isKind is a type guard for the DM rows of one kind, so a test can read that kind's fields.
export const isKind = <K extends DmRow['kind']>(kind: K) => (row: DmRow): row is Extract<DmRow, { kind: K }> => row.kind === kind

// expectKind returns the row, or throws when the row is missing or of another kind.
export function expectKind<K extends DmRow['kind']>(kind: K, row: DmRow | undefined): Extract<DmRow, { kind: K }> {
  if (!row || !isKind(kind)(row)) throw new Error(`expected a ${kind} row`)
  return row
}
