// Group notes count distinct other askers with the same open question.
export function alsoAsked(questions, question) {
  const seen = new Set([question.asker])
  return questions.filter((q) => {
    if (q.state !== 'open' || q.groupKey !== question.groupKey || q.to !== question.to || seen.has(q.asker)) return false
    seen.add(q.asker)
    return true
  })
}

// The human never asks (notify.ErrHumanAnswersOnly), so a post the human
// typed is never a question, even when its text carries the marker.
export function questionID(post) {
  if (post?.human) return ''
  // TODO(legacy): Read old question kinds until retained logs have migrated.
  return ['message', 'question'].includes(post?.kind) ? post.text?.match(/^\[QUESTION (q\d+)\]/)?.[1] ?? '' : ''
}

export const visualKey = (v) => v.id

export function orderQuestions(questions, state) {
  return questions.filter((q) => q.state === state).sort((a, b) => {
    const when = (q) => Date.parse(state === 'answered' ? q.answer?.at ?? q.askedAt : state === 'withdrawn' ? q.withdrawn?.at ?? q.askedAt : q.askedAt)
    const order = (when(a) - when(b)) || Number(a.id.slice(1)) - Number(b.id.slice(1))
    return state === 'open' ? order : -order
  })
}

// answeredLine names who answered. The server's answererName follows the
// answer's human flag: "you" for the human, "root" for an answer from the CLI.
export function answeredLine(q) {
  const who = q.answererName || 'you'
  return `${who[0].toUpperCase()}${who.slice(1)} answered: ${q.answer.choice || q.answer.text}`
}
