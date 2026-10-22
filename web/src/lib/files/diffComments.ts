// Pending owner comments on a diff. They stay in the page until the owner
// sends them as one batch; nothing reaches the server before then.
import type { DiffLine } from './repo.ts'

// A comment the owner wrote on one diff line. Local shape: no shared wire type exists yet.
export interface DiffComment {
  file: string
  line: number
  quote: string
  text: string
}

// commentLine is the line number a comment points at: the new side, or the old side for a removed line.
export const commentLine = (l: Pick<DiffLine, 'a' | 'b' | 't'>): number => l.b || l.a || 0

export const addComment = (pending: DiffComment[], c: DiffComment): DiffComment[] => [...pending, c]

export const sendLabel = (n: number, author?: string): string => `Send ${n} ${n === 1 ? 'comment' : 'comments'}${author ? ` to ${author}` : ''}`

// sendPending delivers the comments pending now in one call and keeps any added while it runs.
export async function sendPending(
  deliver: (comments: DiffComment[]) => unknown,
  getPending: () => DiffComment[],
  setPending: (pending: DiffComment[]) => void,
): Promise<void> {
  const batch = [...getPending()]
  if (!batch.length) return
  await deliver(batch.map(({ file, line, quote, text }) => ({ file, line, quote, text })))
  setPending(getPending().filter((c) => !batch.includes(c)))
}
