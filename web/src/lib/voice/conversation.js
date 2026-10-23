// The conversation inside the call widget: the room's posts on the
// voice-call channel (roomsvc.VoiceChannel), and root's posts to
// @voice-call, since this call started. Each
// call is a fresh conversation, so earlier calls never show here.
import { VOICE } from '../rooms/channels.ts'
import { rootToVoice } from './attention.ts'
import { postParts } from '../ui/postParts.ts'

// NAMES is how the call names who speaks.
export const NAMES = { you: 'you', voice: 'Voice', root: 'root' }

// callPosts keeps the call's posts in order, without repeats.
export function callPosts(posts, startedAt) {
  if (!startedAt) return []
  const seen = new Set()
  return posts
    .filter((p) => (p.channels?.includes(VOICE) || rootToVoice(p)) && p.ts >= startedAt && !seen.has(p.id) && seen.add(p.id))
    .sort((a, b) => a.ts - b.ts)
}

// callRow says who speaks in a post and how it reached the call:
// the owner's spoken words, Voice's spoken reply, Voice's request to root
// (ask), root's answer that Voice relayed, or root's post for Voice to say.
// Voice's own call lines are kind "voice"; its request to root is a normal
// post, addressed to the room because root is the room's owner.
export function callRow(post, roomName) {
  const voice = `${roomName}:voice-call`
  if (post.relayOf) return { who: 'root', to: 'you', tag: post.spoken ? 'spoken' : 'relayed', via: true }
  if (rootToVoice(post)) return { who: 'root', to: 'you', tag: 'for Voice' }
  if (post.from === voice) return post.kind === 'voice' ? { who: 'voice', to: 'you', tag: post.spoken ? 'spoken' : '' } : { who: 'voice', to: 'root', ask: true }
  return { who: 'you', to: 'Voice', tag: post.spoken ? 'spoken' : 'typed' }
}

// askState is the state of work Voice started: answered once a result is
// spoken in the call after it (one result can answer several requests),
// otherwise working.
export function askState(ask, posts, roomName) {
  return posts.some((p) => p.ts > ask.ts && callRow(p, roomName).who === 'root') ? 'answered' : 'working'
}

// WORK is the tool Voice starts work with. Its request shows as its own
// "Started" line, so its tool row shows only when it failed.
export const WORK = 'dispatch'

// TOOLS names Voice's tool calls as the call shows them.
const TOOLS = {
  check_pr: (a) => (a.number ? `Checked PR ${a.number}` : 'Checked a PR'),
  check_merge_queue: () => 'Checked the merge queue',
  answer_question: () => 'Answered your question',
  [WORK]: () => 'Starting the work',
}

// toolLabel is what a tool call did, with its arguments when they matter.
export function toolLabel(name, args) {
  let a = {}
  try { a = JSON.parse(args || '{}') ?? {} } catch { /* no arguments */ }
  return (TOOLS[name] ?? (() => name))(a)
}

// callRows is the call's conversation: its posts and Voice's tool calls,
// in time order. unspoken lists the posts the engine never got Voice to say.
export function callRows(posts, tools, roomName, unspoken = []) {
  const rows = []
  for (const post of posts) {
    const row = { key: post.id, ts: post.ts, post, ...callRow(post, roomName) }
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
export function sharedItems(posts, roomName) {
  return posts.flatMap((post) => postParts(post).attachments.map((att) => ({ key: `share:${post.id}:${att.id}`, ts: post.ts, postId: post.id, who: callRow(post, roomName).who, att })))
}

// shareKind is what a shared file is: a card, an image or a file.
export const shareKind = (att) => (att.render === 'html' ? 'card' : /^image\/(png|jpeg|gif|webp)$/.test(att.mime) ? 'image' : 'file')

// shareWhat is the kind with its article: "a card", "an image", "a file".
export const shareWhat = (att) => ({ card: 'a card', image: 'an image', file: 'a file' })[shareKind(att)]

// shareTitle names a shared file: a card by its title, others by name.
export const shareTitle = (att) => att.title || att.name

// shareLine says who shared what, as the call's one-line peeks show it.
export const shareLine = (share) => `${NAMES[share.who]} shared “${shareTitle(share.att)}”`

// unseen is the shared items the owner has not opened or dismissed, oldest
// first. The banner shows the newest; the bubble's badge counts them all.
export const unseen = (items, seen) => items.filter((i) => !seen.has(i.key))
