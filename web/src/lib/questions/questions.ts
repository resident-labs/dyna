// The fields the question rows read. A question arrives from the host.
export interface QuestionAsk {
  asker: string
  state: string
  groupKey?: string
  to?: string
}

// Group notes count distinct other askers with the same open question.
export function alsoAsked<Q extends QuestionAsk>(questions: Q[], question: Q): Q[] {
  const seen = new Set([question.asker])
  return questions.filter((q) => {
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

// The fields an outcome row reads to place its question in time.
export interface QuestionOrder {
  id: string
  state: string
  askedAt?: string
  answer?: { at?: string }
  withdrawn?: { at?: string }
}

export function orderQuestions<Q extends QuestionOrder>(questions: Q[], state: string): Q[] {
  return questions.filter((q) => q.state === state).sort((a, b) => {
    const when = (q: QuestionOrder) => Date.parse(state === 'answered' ? q.answer?.at ?? q.askedAt ?? '' : state === 'withdrawn' ? q.withdrawn?.at ?? q.askedAt ?? '' : q.askedAt ?? '')
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
