import { writable } from 'svelte/store'
import { api } from '../../api.js'
import type { HeldComment } from '../api/change.ts'
import { errorText } from '../errorText.ts'

interface HeldState {
  comments: HeldComment[]
  loaded: boolean
  error: string
}

export const heldComments = writable<HeldState>({ comments: [], loaded: false, error: '' })
let request = 0

// This separate owner-only endpoint keeps bodies out of shared PR and post data.
export async function refreshHeldComments() {
  const current = ++request
  try {
    const comments: HeldComment[] = (await api.heldPRComments('')) ?? []
    if (current === request) heldComments.set({ comments, loaded: true, error: '' })
  } catch (error) {
    if (current === request) heldComments.update((state) => ({ ...state, loaded: false, error: errorText(error) }))
  }
}

export function removeHeldComment(comment: HeldComment) {
  // A response started before this action must not put the resolved item back.
  request++
  heldComments.update((state) => ({
    ...state,
    comments: state.comments.filter((item) =>
      item.roomId !== comment.roomId || item.key !== comment.key || item.id !== comment.id),
  }))
}
