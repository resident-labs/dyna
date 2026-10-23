// The conversation inside the call widget: the room's posts on the
// voice-call channel (roomsvc.VoiceChannel), and root's posts to
// @voice-call, since this call started. Each
// call is a fresh conversation, so earlier calls never show here.
import { VOICE } from '../rooms/channels.ts'
import { rootToVoice } from './attention.ts'
import { postParts } from '../ui/postParts.ts'
import { isRecord } from '../api/guards.ts'
import type { Attachment } from '../api/room.ts'
import type { ManifestItem } from '../sessions/userText.ts'
import type { VoiceTool } from './callState.ts'

// NAMES is how the call names who speaks.
export const NAMES: Record<Speaker, string> = { you: 'you', voice: 'Voice', root: 'root' }

// Speaker is one voice in the call.
export type Speaker = 'you' | 'voice' | 'root'

// CallPost is the part of a room post the call reads.
export interface CallPost {
  id: string
  ts: number
  from?: string
  to?: string[] | null
  text?: string
  kind?: string
  channels?: string[]
  spoken?: boolean
  human?: boolean
  source?: string
  relayOf?: string
  attachments?: Attachment[]
}

// SpeechPost is a post as callRow reads it: who sent it, to whom, and how.
export interface SpeechPost {
  from?: string
  to?: string[] | null
  kind?: string
  spoken?: boolean
  human?: boolean
  relayOf?: string
  source?: string
}

// CallSpeech is who speaks in a post and how it reached the call.
export interface CallSpeech {
  who: Speaker
  to: string
  tag?: string
  via?: boolean
  ask?: boolean
}

// SharedFile is a post's file: a stored attachment, or one the engine listed
// in a post's text.
export type SharedFile = Attachment | ManifestItem

// SharedItem is one file posted in the call, and who shared it.
export interface SharedItem {
  key: string
  ts: number
  postId: string
  who: Speaker
  att: SharedFile
}

// CallRow is one line of the call's conversation: a post, a shared file or
// a tool call.
export interface CallRow {
  key: string
  ts: number
  post?: CallPost
  share?: SharedItem
  tool?: { label: string; state: string }
  who?: Speaker
  to?: string
  tag?: string
  via?: boolean
  ask?: boolean
  state?: 'working' | 'answered'
}

// rootToVoicePost is rootToVoice for a stored post, which has null for no
// recipients.
const rootToVoicePost = (p: SpeechPost): boolean => rootToVoice({ to: p.to ?? undefined, from: p.from, source: p.source })

// callPosts keeps the call's posts in order, without repeats.
export function callPosts(posts: CallPost[], startedAt: number): CallPost[] {
  if (!startedAt) return []
  const seen = new Set<string>()
  return posts
    .filter((p) => (p.channels?.includes(VOICE) || rootToVoicePost(p)) && p.ts >= startedAt && !seen.has(p.id) && seen.add(p.id))
    .sort((a, b) => a.ts - b.ts)
}

// callRow says who speaks in a post and how it reached the call:
// the owner's spoken words, Voice's spoken reply, Voice's request to root
// (ask), root's answer that Voice relayed, or root's post for Voice to say.
// Voice's own call lines are kind "voice"; its request to root is a normal
// post, addressed to the room because root is the room's owner.
export function callRow(post: SpeechPost, roomName: string): CallSpeech {
  const voice = `${roomName}:voice-call`
  if (post.relayOf) return { who: 'root', to: 'you', tag: post.spoken ? 'spoken' : 'relayed', via: true }
  if (rootToVoicePost(post)) return { who: 'root', to: 'you', tag: 'for Voice' }
  if (post.from === voice) return post.kind === 'voice' ? { who: 'voice', to: 'you', tag: post.spoken ? 'spoken' : '' } : { who: 'voice', to: 'root', ask: true }
  return { who: 'you', to: 'Voice', tag: post.spoken ? 'spoken' : 'typed' }
}

// askState is the state of work Voice started: answered once a result is
// spoken in the call after it (one result can answer several requests),
// otherwise working.
export function askState(ask: { ts: number }, posts: CallPost[], roomName: string): 'working' | 'answered' {
  return posts.some((p) => p.ts > ask.ts && callRow(p, roomName).who === 'root') ? 'answered' : 'working'
}

// WORK is the tool Voice starts work with. Its request shows as its own
// "Started" line, so its tool row shows only when it failed.
export const WORK = 'dispatch'

// ToolArgs are the arguments of one of Voice's tool calls, as JSON.
type ToolArgs = Record<string, unknown>

// TOOLS names Voice's tool calls as the call shows them.
const TOOLS: Record<string, (a: ToolArgs) => string> = {
  check_pr: (a) => (a.number ? `Checked PR ${a.number}` : 'Checked a PR'),
  check_merge_queue: () => 'Checked the merge queue',
  answer_question: () => 'Answered your question',
  [WORK]: () => 'Starting the work',
}

// toolLabel is what a tool call did, with its arguments when they matter.
export function toolLabel(name: string, args: string): string {
  let a: ToolArgs = {}
  try {
    const parsed: unknown = JSON.parse(args || '{}')
    if (isRecord(parsed)) a = parsed
  } catch { /* no arguments */ }
  return (TOOLS[name] ?? (() => name))(a)
}

// callRows is the call's conversation: its posts and Voice's tool calls,
// in time order. unspoken lists the posts the engine never got Voice to say.
export function callRows(posts: CallPost[], tools: VoiceTool[], roomName: string, unspoken: string[] = []): CallRow[] {
  const rows: CallRow[] = []
  for (const post of posts) {
    const row: CallRow = { key: post.id, ts: post.ts, post, ...callRow(post, roomName) }
    if (row.ask) row.state = askState(post, posts, roomName)
    if (unspoken.includes(post.id)) row.tag = 'not spoken'
    const shared = sharedItems([post], roomName)
    // A post that only shares files shows as its shared lines.
    if (post.text?.trim() || !shared.length) rows.push(row)
    rows.push(...shared.map((share) => ({ key: share.key, ts: share.ts, share })))
  }
  for (const t of tools) {
    if (t.name === WORK && t.state !== 'failed') continue
    rows.push({ key: `tool:${t.id}`, ts: t.ts, tool: { label: toolLabel(t.name, t.args), state: t.state } })
  }
  return rows.sort((a, b) => a.ts - b.ts)
}

// sharedItems lists the files posted in the call, oldest first: each
// attachment of a call post, with who shared it. key names one item.
export function sharedItems(posts: CallPost[], roomName: string): SharedItem[] {
  return posts.flatMap((post) => postParts(post).attachments.map((att) => ({ key: `share:${post.id}:${att.id}`, ts: post.ts, postId: post.id, who: callRow(post, roomName).who, att })))
}

// ShareKind is what a shared file is.
export type ShareKind = 'card' | 'image' | 'file'

// shareKind is what a shared file is: a card, an image or a file.
export const shareKind = (att: SharedFile): ShareKind => (att.render === 'html' ? 'card' : /^image\/(png|jpeg|gif|webp)$/.test(att.mime) ? 'image' : 'file')

const WHATS: Record<ShareKind, string> = { card: 'a card', image: 'an image', file: 'a file' }

// shareWhat is the kind with its article: "a card", "an image", "a file".
export const shareWhat = (att: SharedFile): string => WHATS[shareKind(att)]

// shareTitle names a shared file: a card by its title, others by name.
export const shareTitle = (att: SharedFile): string => att.title || att.name

// shareLine says who shared what, as the call's one-line peeks show it.
export const shareLine = (share: SharedItem): string => `${NAMES[share.who]} shared “${shareTitle(share.att)}”`

// unseen is the shared items the owner has not opened or dismissed, oldest
// first. The banner shows the newest; the bubble's badge counts them all.
export const unseen = (items: SharedItem[], seen: Set<string>): SharedItem[] => items.filter((i) => !seen.has(i.key))
