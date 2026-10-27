// The owner's side of a terminal request: read the exact command, run it as
// filed, and the exit reports itself. Nothing runs until the owner clicks Run,
// and the agent gets lines of the screen only from a follow-up the owner chose
// after seeing them.
import type { Question, TerminalVerdict } from '../api/room.ts'
import type { TermInfo } from '../api/terminal.ts'

// A request the owner may act on: terminal, open, and past every holder.
export const waitsForOwner = (q: Question): boolean => q.kind === 'terminal' && q.state === 'open' && q.terminal?.stage === 'waiting-owner'

export const ownerRequests = (questions: readonly Question[]): Question[] => questions.filter(waitsForOwner)


// quoteArg shows one argument as a shell would need it typed.
export function quoteArg(arg: string): string {
  if (arg !== '' && /^[\w@%+=:,./~-]+$/.test(arg)) return arg
  return `'${arg.replaceAll("'", `'\\''`)}'`
}

export const commandLine = (argv: readonly string[] | null | undefined): string => (argv ?? []).map(quoteArg).join(' ')

// SentReport is the result the dock sent to the filer when the command exited.
export interface SentReport {
  requestId: string
  askerName: string
  verdict: 'done' | 'failed'
  // code is the exit code; -1 means the shell was lost.
  code: number
}

// autoVerdict is the report for an exit code: 0 is done, anything else failed.
export const autoVerdict = (code: number): 'done' | 'failed' => (code === 0 ? 'done' : 'failed')

// sentLine is the one quiet line that replaces the report form.
export const sentLine = (s: SentReport): string =>
  `Sent to ${s.askerName}: ${s.verdict} (${s.code < 0 ? 'shell lost' : `exit ${s.code}`})`

// followUpText is the message that adds a note, and the lines the owner saw, to
// a result already sent.
export function followUpText(s: SentReport, note: string, lines: string | null): string {
  const parts = [`@${s.askerName} Follow-up on terminal request ${s.requestId} (${s.verdict}).`]
  if (note.trim()) parts.push(note.trim())
  if (lines !== null) parts.push(`Last 20 lines of the terminal:\n\`\`\`\n${lines.trimEnd()}\n\`\`\``)
  return parts.join('\n\n')
}

// RequestApi is the engine's three owner calls for one request.
export interface RequestApi {
  run(): Promise<TermInfo>
  preview(): Promise<string>
  report(verdict: TerminalVerdict, note: string, shareLines: boolean): Promise<unknown>
}

// requestActions is what the buttons and the exit call. Making it calls nothing.
export function requestActions(api: RequestApi) {
  return {
    run: () => api.run(),
    decline: (reason: string) => api.report('declined', reason.trim(), false),
    preview: () => api.preview(),
  }
}
