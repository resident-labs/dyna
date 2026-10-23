import type { Member } from '../api/room.ts'

// Fields the tree reads. Extra member fields stay on the returned rows.
// depth is optional on the input because orderMemberSessions replaces it.
export type MemberView = Partial<Pick<Member, 'id' | 'parent' | 'status' | 'closed' | 'depth'>>

type FoldRow<M> = { member: M; hasChildren: boolean; folded: boolean; hidden: number }

// depth is computed for this list. The input row's own depth does not survive.
type Placed<M> = M & { depth: number }

// A map lookup must accept an absent id. Map.get(undefined) is part of the walk.
export interface MemberLookup<M> {
  get(id: string | undefined): M | undefined
}

export function isFinished(member?: MemberView | null): boolean {
  return !!member && member.status === 'dormant' && !member.closed
}

// The backend supplies preorder sessions. Retain each sibling group's order,
// place its finished members last, and emit each member's complete subtree.
// A parent omitted from the visible list (for example, closed) leaves a root.
// Each member gets the depth it has in this list, so a root shows at depth 0
// whatever depth the backend recorded under its missing parent.
export function orderMemberSessions<M extends MemberView>(sessions: readonly M[]): Placed<M>[] {
  const ids = new Set(sessions.map((member) => member.id))
  const children = new Map<string | undefined, M[]>()
  for (const member of sessions) {
    const parent = ids.has(member.parent) ? member.parent : ''
    const siblings = children.get(parent)
    if (siblings) siblings.push(member)
    else children.set(parent, [member])
  }

  const ordered: Placed<M>[] = []
  function visit(parent: string | undefined, depth: number): void {
    const siblings = children.get(parent) ?? []
    for (const rank of [0, 1]) {
      for (const member of siblings) {
        const r = isFinished(member) ? 1 : 0
        if (r !== rank) continue
        ordered.push({ ...member, depth })
        visit(member.id, depth + 1)
      }
    }
  }
  visit('', 0)
  return ordered
}

// openTrail lists the sessions that show their children by default: the
// focused session and its ancestors. A feed view focuses its lead, so its
// team is open and every other team stays folded.
export function openTrail<M extends MemberView>(sessions: readonly M[], focus?: string): Set<string | undefined> {
  return new Set(ancestors(new Map(sessions.map((member): [string | undefined, M] => [member.id, member])), focus).map((member) => member.id))
}

// ancestors lists a session and its parents by session ID, top level first.
// The feed view list and the member list both walk the agent tree with it. A
// loop in stored parents stops at the first repeat.
export function ancestors<M extends MemberView>(byId: MemberLookup<M>, id?: string): M[] {
  const out: M[] = []
  for (let member = byId.get(id); member && !out.includes(member); member = byId.get(member.parent)) out.unshift(member)
  return out
}

// foldMembers hides the subtree of each session that open(id) closes. The
// input is preorder (see orderMemberSessions). Each visible row records
// whether it has children and, when folded, how many hidden descendants it
// holds and how many of them are live, so folded work stays visible.
export function foldMembers<M extends MemberView>(ordered: readonly M[], open: (id: string | undefined) => boolean): FoldRow<M>[] {
  const ids = new Set(ordered.map((member) => member.id))
  const parents = new Set(ordered.map((member) => member.parent).filter((id) => ids.has(id)))
  const rows: FoldRow<M>[] = []
  const rowOf = new Map<string | undefined, FoldRow<M>>()
  const anchor = new Map<string | undefined, FoldRow<M>>() // hidden session id -> the visible row that folds it
  for (const member of ordered) {
    const parent = ids.has(member.parent) ? member.parent : ''
    const holder = !parent ? null : anchor.get(parent) ?? (open(parent) ? null : rowOf.get(parent))
    if (holder) {
      anchor.set(member.id, holder)
      holder.hidden++
      continue
    }
    const hasChildren = parents.has(member.id)
    const row = { member, hasChildren, folded: hasChildren && !open(member.id), hidden: 0 }
    rows.push(row)
    rowOf.set(member.id, row)
  }
  return rows
}

// finishedTrees lists the sessions of each top-level tree that has a finished
// (dormant) session and no other open session. The member list moves these trees into
// a folded Finished group.
export function finishedTrees<M extends MemberView>(ordered: readonly M[]): Set<string | undefined> {
  const ids = new Set(ordered.map((member) => member.id))
  const root = new Map<string | undefined, string | undefined>()
  const finished = new Set<string | undefined>()
  const open = new Set<string | undefined>()
  for (const member of ordered) {
    const top = ids.has(member.parent) ? root.get(member.parent) : member.id
    root.set(member.id, top)
    if (isFinished(member)) finished.add(top)
    else if (!member.closed) open.add(top)
  }
  return new Set(ordered.filter((member) => finished.has(root.get(member.id)) && !open.has(root.get(member.id))).map((member) => member.id))
}
