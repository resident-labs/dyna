import type { Attachment, Post } from '../api/room.ts'
import { short } from './format.ts'
import { ownerName } from '../settings/owner.ts'

export const postIDPattern = /^[A-Za-z0-9_-]{1,64}$/
export const roomIDPattern = /^(?:global|room_\d{8}-\d{6}-[a-f0-9]{8})$/

// Every view of a stored post points to the same room and post IDs.
export function postHref(room: string, post: string) {
  if (!roomIDPattern.test(room || '') || !postIDPattern.test(post || '')) return ''
  return '#' + new URLSearchParams({ room, post }).toString()
}

// Viewer metadata belongs to the selected post, not the stored attachment.
// One attachment can appear on more than one post.
export function postAttachment<T extends Partial<Attachment> & { postId?: string }>(attachment: T, post: string): Omit<T, "postId"> & { postId?: string } {
  return postIDPattern.test(post || '') ? { ...attachment, postId: post } : attachment
}

// speaker names a post's sender like roomsvc.SpeakerLabel, except that a post
// the human owner typed shows the owner's chosen name, not "you": "root" for
// other owner posts, and the member's short name otherwise. A label from the
// server wins, such as "workflow <name>". Name, avatar and alignment all
// follow this one label.
export function speaker(post: Partial<Pick<Post, 'from' | 'human' | 'speaker'>> | undefined, roomName?: string) {
  if (!post) throw new TypeError('A post is required to name its speaker.')
  if (post.from === roomName && post.human) return ownerName()
  return post.speaker || short(post.from, roomName)
}

