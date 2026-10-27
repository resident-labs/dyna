import type { Post, Question } from '../api/room.ts'

// The fields of a card's post that decide where its input goes.
export type CardPost = Pick<Post, 'id'> & Partial<Pick<Post, 'conversation' | 'reply'>>

export type SubmitRoute =
  | { kind: 'answer'; question: string; choice: string; text: string }
  | { kind: 'reply'; recipient: string; text: string; replyTo: string; conversation: string }
  | { kind: 'refuse'; error: string }

// replyDraft is the composer text for a confirmed reply. The recipient stays a
// field on the route. The @ prefix exists only in the draft the post API parses.
export function replyDraft(route: { recipient: string; text: string }): string {
  return `@${route.recipient} ${route.text}`
}

// routeSubmit decides what a card's dyna:submit does. A card on the post of an
// open question answers that question: with the choice when it is one of the
// question's options, else with the text. A question that is no longer open
// refuses with its state ("answered"). Any other card posts the text as an
// owner reply to the card's author, in the post's own conversation, so the
// author wakes. The recipient is a field, not a prefix inside the reply text.
export function routeSubmit(
  post: CardPost,
  questions: readonly Pick<Question, 'id' | 'post' | 'state' | 'options'>[],
  submit: { text: string; choice?: string },
): SubmitRoute {
  const text = submit.text.trim()
  const question = questions.find((q) => q.post === post.id)
  if (question) {
    if (question.state !== 'open') return { kind: 'refuse', error: question.state }
    const choice = submit.choice !== undefined && question.options?.includes(submit.choice) ? submit.choice : ''
    if (!choice && !text) return { kind: 'refuse', error: 'empty' }
    return { kind: 'answer', question: question.id, choice, text: choice ? '' : text }
  }
  if (!text) return { kind: 'refuse', error: 'empty' }
  const address = post.reply?.address
  if (!address) return { kind: 'refuse', error: 'no-recipient' }
  return { kind: 'reply', recipient: address, text, replyTo: post.id, conversation: post.conversation ?? '' }
}
