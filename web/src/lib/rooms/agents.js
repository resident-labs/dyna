import { isFinished, orderMemberSessions } from './memberOrder.js'

// activeAgents groups the awake agents of every active room for the agents
// list. Dormant (finished) agents and agents in paused rooms only add to
// hidden. Closed and service sessions are not agents the owner talks to.
export function activeAgents(rooms, membersByRoom) {
  // membersByRoom is null until the member lists first load: the view is
  // loading, and the working count comes from the room summaries the rail uses.
  if (!membersByRoom) return { groups: [], hidden: 0, working: roomsWorking(rooms), loading: true }
  const groups = []
  let hidden = 0
  for (const room of rooms) {
    if (room.archived) continue
    const open = (membersByRoom[room.id] ?? []).filter((m) => !m.owner && !m.closed && !m.service)
    const awake = room.paused ? [] : open.filter((m) => !isFinished(m))
    hidden += open.length - awake.length
    if (awake.length) groups.push({ room, members: orderMemberSessions(awake).map((m) => ({ ...m, roomId: room.id })) })
  }
  const working = groups.reduce((n, g) => n + g.members.filter((m) => m.state === 'live').length, 0)
  return { groups, hidden, working, loading: false }
}

// roomsWorking counts working agents from the room summaries: the rail's
// count, and the agents view's count before member lists load.
export function roomsWorking(rooms) {
  return rooms.filter((r) => !r.archived && !r.paused).reduce((n, r) => n + (r.running ?? 0), 0)
}

// agentsHeader is the Home header's text. It never waits on member lists,
// which load only while the agents sidebar shows: until then it gives just
// the working count, which the room summaries always know.
export function agentsHeader(agents) {
  const n = agents.groups.length
  return { rooms: agents.loading ? '' : `${n} room${n === 1 ? '' : 's'} with agents`, working: agents.working }
}
