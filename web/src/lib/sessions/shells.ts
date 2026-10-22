// The shells a session runs now. Jobs name their owner by session id or address.
import { isOpen } from './shellBlock.ts'
import type { ShellJob } from './shellBlock.ts'

export interface ShellOwner {
  id?: string
  address?: string
}

export function runningShells(jobs: ShellJob[] | null, member: ShellOwner | null): ShellJob[] {
  if (!member || !jobs) return []
  return jobs.filter((job) => isOpen(job) && !!job.owner && (job.owner === member.id || job.owner === member.address))
}
