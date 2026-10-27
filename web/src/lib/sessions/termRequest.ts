// The owner's side of a terminal request: read the exact command, change it
// if needed, run it, then report. Nothing runs until the owner clicks Run, and
// the agent gets lines of the screen only when the owner ticked the box after
// seeing them.
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

// splitCommand reads an edited command line back into arguments: single and
// double quotes and backslashes, no expansion. A line the owner cannot have
// meant (an open quote) is an error, not a guess.
export function splitCommand(line: string): { argv: string[] } | { error: string } {
  const argv: string[] = []
  let cur = ''
  let has = false
  let quote = ''
  for (let i = 0; i < line.length; i++) {
    const c = line[i] ?? ''
    if (quote === "'") {
      if (c === "'") quote = ''
      else cur += c
    } else if (quote === '"') {
      if (c === '"') quote = ''
      else if (c === '\\' && i + 1 < line.length && '"\\$`'.includes(line[i + 1] ?? '')) cur += line[++i] ?? ''
      else cur += c
    } else if (c === "'" || c === '"') {
      quote = c
      has = true
    } else if (c === '\\' && i + 1 < line.length) {
      cur += line[++i] ?? ''
      has = true
    } else if (/\s/.test(c)) {
      if (cur !== '' || has) argv.push(cur)
      cur = ''
      has = false
    } else {
      cur += c
      has = true
    }
  }
  if (quote) return { error: `The command has an unclosed ${quote === "'" ? 'single' : 'double'} quote.` }
  if (cur !== '' || has) argv.push(cur)
  if (!argv.length) return { error: 'The command is empty.' }
  return { argv }
}

// editedArgv is the command to send with Run: null while the owner did not
// change the filed one, so the engine runs exactly what was filed.
export function editedArgv(filed: readonly string[] | null | undefined, text: string): { argv: string[] | null } | { error: string } {
  if (text.trim() === commandLine(filed)) return { argv: null }
  const parsed = splitCommand(text)
  return 'error' in parsed ? parsed : { argv: parsed.argv }
}

// ResultForm is the report banner: a note, and the choice to share lines.
export interface ResultForm {
  note: string
  share: boolean
  // lines is what the owner was shown; null until they asked to see it.
  lines: string | null
}

export const emptyForm = (): ResultForm => ({ note: '', share: false, lines: null })

// sharesLines is true only for lines the owner ticked and was shown.
export const sharesLines = (f: ResultForm): boolean => f.share && f.lines !== null

// RequestApi is the engine's three owner calls for one request.
export interface RequestApi {
  run(argv: string[] | null): Promise<TermInfo>
  preview(): Promise<string>
  report(verdict: TerminalVerdict, note: string, shareLines: boolean): Promise<unknown>
}

// requestActions is what the buttons call. Making it calls nothing.
export function requestActions(api: RequestApi) {
  return {
    run: (argv: string[] | null) => api.run(argv),
    decline: (reason: string) => api.report('declined', reason.trim(), false),
    preview: () => api.preview(),
    report: (verdict: 'done' | 'failed', f: ResultForm) => api.report(verdict, f.note.trim(), sharesLines(f)),
  }
}
