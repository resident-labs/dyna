// The shells a session runs now. Jobs name their owner by session id or address.
import { isOpen } from './shell.ts'
import type { ShellJob } from './shell.ts'

export interface ShellOwner {
  id?: string
  address?: string
}

export function runningShells(jobs: ShellJob[] | null, member: ShellOwner | null): ShellJob[] {
  if (!member || !jobs) return []
  return jobs.filter((job) => isOpen(job) && !!job.owner && (job.owner === member.id || job.owner === member.address))
}

// sessionShellJobs is the Shell tab list: this session's jobs, plus the job a
// link selected when it is not already in that list.
export function sessionShellJobs(jobs: ShellJob[] | null, member: ShellOwner | null, selected = ''): ShellJob[] {
  if (!jobs) return []
  const mine = member ? jobs.filter((job) => !!job.owner && (job.owner === member.id || job.owner === member.address)) : []
  const chosen = jobs.find((job) => job.id === selected)
  if (chosen && !mine.some((job) => job.id === chosen.id)) return [chosen, ...mine]
  return mine
}
