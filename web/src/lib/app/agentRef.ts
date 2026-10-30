// A lightweight link to a session: a machine's usage row or a run's agent gives
// one, without the fields a member row carries. The room roster supplies the
// rest, so the session tab, the preview and the feed read the right computer.

/** The part of a session that opening its view needs. */
export interface AgentRef {
  id?: string
  name: string
  hue: number
  address?: string
  room?: string
  roomId?: string
  computer?: string
}

/** The roster member for the reference, or the reference itself with its computer named. */
export function resolveAgent<M extends { id?: string }>(ref: AgentRef, roster: M[]): M | AgentRef {
  const member = ref.id ? roster.find((m) => m.id === ref.id) : undefined
  // A session's computer is named by its session ID (computers.ActorID).
  return member ?? { ...ref, computer: ref.id }
}
