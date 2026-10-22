// What asks for the owner's attention during a call: root's open questions.
// Ringing comes from call requests (callRequests.ts), never from a post.

const VOICE = ':voice-call'

// rootToVoice says whether a post is root speaking to Voice: a post to
// <room>:voice-call that is not the call's own (Voice's lines and the
// owner's speech carry source voice or come from Voice). During a call
// Voice speaks it; with no call it is an ordinary post and rings nobody.
export function rootToVoice(p: { to?: string[]; from?: string; source?: string }) {
  const to = p.to?.find((a) => a.endsWith(VOICE))
  return !!to && p.from !== to && p.source !== 'voice'
}

// rootQuestion is root's oldest open question to the owner in a room.
export function rootQuestion<T extends { state: string; to: string; askerName: string; askedAt: string }>(list: T[] | null | undefined) {
  return (list ?? [])
    .filter((q) => q.state === 'open' && q.to === 'owner' && q.askerName === 'root')
    .sort((a, b) => Date.parse(a.askedAt) - Date.parse(b.askedAt))[0] ?? null
}

// answers are the card's quick answers: the question's options, sent as
// the choice, or else Yes and No, sent as text. The question store accepts
// a choice only from the question's options.
export function answers(q: { options?: string[] }) {
  if (q.options?.length) return q.options.map((label) => ({ label, choice: label, text: '' }))
  return ['Yes', 'No'].map((label) => ({ label, choice: '', text: label }))
}
