// The fields the question rows read. A question arrives from the host.
export interface QuestionAsk {
  asker: string
  state: string
  kind?: string
  groupKey?: string
  to?: string
}

// Group notes count distinct other askers with the same open question.
export function alsoAsked<Q extends QuestionAsk>(questions: Q[], question: Q): Q[] {
  const seen = new Set([question.asker])
  return questions.filter((q) => {
    if (question.kind === 'terminal' || q.kind === 'terminal') return false
    if (q.state !== 'open' || q.groupKey !== question.groupKey || q.to !== question.to || seen.has(q.asker)) return false
    seen.add(q.asker)
    return true
  })
}

// The fields a post needs to name the question it opens.
export interface QuestionPost {
  human?: boolean
  kind?: string
  text?: string
}

// The human never asks (notify.ErrHumanAnswersOnly), so a post the human
// typed is never a question, even when its text carries the marker.
export function questionID(post?: QuestionPost | null): string {
  if (post?.human) return ''
  // TODO(legacy): Read old question kinds until retained logs have migrated.
  return ['message', 'question'].includes(post?.kind ?? '') ? post?.text?.match(/^\[QUESTION (q\d+)\]/)?.[1] ?? '' : ''
}

export const visualKey = (v: { id: string }) => v.id

// RequestFilter is the inbox tab that owns a row. A terminal request that a
// session still holds stays out of the owner's inbox. Every closed terminal
// request belongs with answered requests, including a withdrawn one.
export type RequestFilter = 'open' | 'answered' | 'withdrawn' | ''

export interface RequestState {
  state: string
  kind?: string
  terminal?: { stage?: string }
}

export function requestFilter<Q extends RequestState>(question: Q): RequestFilter {
  if (question.kind === 'terminal') {
    if (question.state !== 'open') return 'answered'
    return question.terminal?.stage === 'waiting-owner' ? 'open' : ''
  }
  switch (question.state) {
    case 'open': return 'open'
    case 'answered': return 'answered'
    case 'withdrawn': return 'withdrawn'
    default: return ''
  }
}

// ownerRequestCount is the one sidebar badge: plain questions and terminal
// requests that wait for the owner, never requests another session still holds.
export function ownerRequestCount<Q extends RequestState & { to: string }>(questions: Q[]): number {
  return questions.filter((q) => q.to === 'owner' && requestFilter(q) === 'open').length
}

// terminalCommand quotes each argument for review without changing the argv
// that the owner call runs. Plain arguments stay readable; empty arguments and
// arguments with shell syntax or whitespace use single quotes.
export function terminalCommand(argv: readonly string[] | null | undefined): string {
  return (argv ?? []).map((arg) => {
    if (/^[A-Za-z0-9_@%+=:,./-]+$/.test(arg)) return arg
    return `'${arg.replaceAll("'", `'"'"'`)}'`
  }).join(' ')
}

// The fields an outcome row reads to place its question in time.
export interface QuestionOrder extends RequestState {
  id: string
  state: string
  askedAt?: string
  answer?: { at?: string }
  withdrawn?: { at?: string }
}

export function orderQuestions<Q extends QuestionOrder>(questions: Q[], state: string): Q[] {
  return questions.filter((q) => requestFilter(q) === state).sort((a, b) => {
    const when = (q: QuestionOrder) => Date.parse(state === 'answered' ? q.answer?.at ?? q.withdrawn?.at ?? q.askedAt ?? '' : state === 'withdrawn' ? q.withdrawn?.at ?? q.askedAt ?? '' : q.askedAt ?? '')
    const order = (when(a) - when(b)) || Number(a.id.slice(1)) - Number(b.id.slice(1))
    return state === 'open' ? order : -order
  })
}

// answeredLine names who answered. The server's answererName follows the
// answer's human flag: "you" for the human, "root" for an answer from the CLI.
export function answeredLine(q: { answererName?: string; answer: { choice?: string; text?: string } }): string {
  const who = q.answererName || 'you'
  return `${who[0]?.toUpperCase() ?? ''}${who.slice(1)} answered: ${q.answer.choice || q.answer.text}`
}
