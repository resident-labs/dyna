// Where a shell-job log opens. A job belongs to the session that started it.
import { detailIDs } from '../ui/details.ts'
import { ROOT_SESSION } from '../ui/route.ts'

export interface ShellMember {
  id?: string
  address?: string
}

export interface ShellDestination {
  session: string
  jobId: string
  tab: 'shell'
}

// shellDestination is the session Shell tab for this job.
// A session id, or a member with that id or address, opens that session.
// A root, hook, or owner shell has no session, so it opens on the root seat.
export function shellDestination(jobId: string, owner: string | undefined, members: readonly ShellMember[]): ShellDestination {
  const who = owner?.trim() ?? ''
  const member = who ? members.find((m) => m.id === who || (!!m.address && m.address === who)) : undefined
  if (member?.id && detailIDs.session.test(member.id)) return { session: member.id, jobId, tab: 'shell' }
  if (detailIDs.session.test(who)) return { session: who, jobId, tab: 'shell' }
  return { session: ROOT_SESSION, jobId, tab: 'shell' }
}
