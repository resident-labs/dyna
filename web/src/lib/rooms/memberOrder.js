export function isFinished(member) {
  return member?.status === 'dormant' && !member.closed
}

// The backend supplies preorder sessions. Retain each sibling group's order,
// place its finished members last, and emit each member's complete subtree.
// A parent omitted from the visible list (for example, closed) leaves a root.
// Each member gets the depth it has in this list, so a root shows at depth 0
// whatever depth the backend recorded under its missing parent.
export function orderMemberSessions(sessions) {
  const ids = new Set(sessions.map((member) => member.id))
  const children = new Map()
  for (const member of sessions) {
    const parent = ids.has(member.parent) ? member.parent : ''
    if (!children.has(parent)) children.set(parent, [])
    children.get(parent).push(member)
  }

  const ordered = []
  function visit(parent, depth) {
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
// focused session and its ancestors. A channel view focuses its lead, so its
// team is open and every other team stays folded.
export function openTrail(sessions, focus) {
  return new Set(ancestors(new Map(sessions.map((member) => [member.id, member])), focus).map((member) => member.id))
}

// ancestors lists a session and its parents by session ID, top level first.
// The channel list and the member list both walk the agent tree with it. A
// loop in stored parents stops at the first repeat.
export function ancestors(byId, id) {
  const out = []
  for (let member = byId.get(id); member && !out.includes(member); member = byId.get(member.parent)) out.unshift(member)
  return out
}

// foldMembers hides the subtree of each session that open(id) closes. The
// input is preorder (see orderMemberSessions). Each visible row records
// whether it has children and, when folded, how many hidden descendants it
// holds and how many of them are live, so folded work stays visible.
export function foldMembers(ordered, open) {
  const ids = new Set(ordered.map((member) => member.id))
  const parents = new Set(ordered.map((member) => member.parent).filter((id) => ids.has(id)))
  const rows = []
  const rowOf = new Map()
  const anchor = new Map() // hidden session id -> the visible row that folds it
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
export function finishedTrees(ordered) {
  const ids = new Set(ordered.map((member) => member.id))
  const root = new Map()
  const finished = new Set()
  const open = new Set()
  for (const member of ordered) {
    const top = ids.has(member.parent) ? root.get(member.parent) : member.id
    root.set(member.id, top)
    if (isFinished(member)) finished.add(top)
    else if (!member.closed) open.add(top)
  }
  return new Set(ordered.filter((member) => finished.has(root.get(member.id)) && !open.has(root.get(member.id))).map((member) => member.id))
}
