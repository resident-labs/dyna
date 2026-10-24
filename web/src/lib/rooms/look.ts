import type { EventPayload } from './events.gen.ts'

type ChoiceID = 'highlight' | 'finished' | 'prs' | 'goals'
type ChoiceValue = string | boolean | null
interface Choice {
  id: ChoiceID
  label: string
  hint: string
  options?: [string, string][]
}
type Values = Partial<Record<ChoiceID, ChoiceValue>>
export interface Look {
  all: Values
}
interface FeedPost {
  id?: string
  speaker?: string
  kind?: string
  notice?: { kind?: string } | null
  // payload is the typed JSON of a sealed system event. The one field this
  // file reads is its state, which names a failed or stopped result.
  payload?: EventPayload | null
  completion?: boolean
  owner?: boolean
  // forRoot marks a post addressed to the room's root session, which the owner
  // reads as a post to them (internal/roomsvc.Post.ForRoot).
  forRoot?: boolean
  to?: string[]
}

// How a feed looks. The owner sets these choices once in Settings, and every
// feed view follows them. A choice with options picks one of them; one without
// is on or off.
export const CHOICES: Choice[] = [
  { id: 'highlight', label: 'Highlight', hint: 'Posts that get the gold mark', options: [['root', 'Root only'], ['all', 'Everyone to you'], ['off', 'Off']] },
  { id: 'finished', label: 'Finished turns', hint: 'Root’s results always show in full. Failures always show.', options: [['full', 'Full'], ['line', 'One line'], ['hidden', 'Hidden']] },
  { id: 'prs', label: 'Pull request updates', hint: 'Opened, reviewed, checked, merged' },
  { id: 'goals', label: 'Goal updates', hint: 'Goals added, done or dropped' },
]

const DEFAULTS: Record<ChoiceID, string | boolean> = { highlight: 'root', finished: 'line', prs: true, goals: true }

// setting resolves one choice: the saved value, else the built-in default.
export function setting(look: Look, id: ChoiceID) {
  return look.all?.[id] ?? DEFAULTS[id]
}

// readLook reads the look a browser saved. An older version also saved
// per-view overrides under "channels"; they no longer apply, so only the
// choices for every feed view are kept.
// TODO: legacy/shim remove later — the old "channels" key is dropped, not read.
export function readLook(raw: string | null): Look {
  try {
    const saved = JSON.parse(raw || '{}')
    return { all: saved?.all ?? {} }
  } catch {
    return { all: {} }
  }
}

const fromRoot = (post: FeedPost) => post.speaker === 'root'

// resultState is how an automatic result says it ended: the state of its typed
// payload, or none for a post that carries no payload.
const resultState = (post: FeedPost): string => {
  const payload = post.payload
  if (!payload || typeof payload !== 'object' || !('state' in payload)) return ''
  return String(payload.state)
}

// failedCompletion reports a turn or result that failed or was stopped. Both
// always show in full, whatever the Finished turns setting says. A typed event
// keeps its state in the payload; an older completion names it as its kind.
// TODO: legacy/shim remove later — the kind form reads a completion stored
// before the typed turn events. Remove when that history ages out.
const failedCompletion = (post: FeedPost) => {
  const state = resultState(post)
  return state === 'failed' || state === 'stopped' ||
    post.notice?.kind === 'failed' || post.notice?.kind === 'stopped' || post.kind === 'failed' || post.kind === 'stopped'
}

// kindOf names the choice that controls a post, or '' for a post that always shows.
export function kindOf(post: FeedPost): ChoiceID | '' {
  if (post.kind === 'pr') return 'prs'
  if (post.kind === 'goal') return 'goals'
  if (post.completion && !failedCompletion(post) && !fromRoot(post)) return 'finished'
  return ''
}

// visibleTo filters a feed. A post in revealed was asked for by a jump, so it
// shows even when its kind is hidden.
export function visibleTo(look: Look, revealed: Record<string, boolean> = {}) {
  return (post: FeedPost) => {
    const k = kindOf(post)
    if (!k || (post.id !== undefined && revealed[post.id] === true)) return true
    const v = setting(look, k)
    return k === 'finished' ? v !== 'hidden' : v
  }
}

// oneLine reports a finished turn the feed shows as a single line.
export const oneLine = (look: Look, post: FeedPost) => kindOf(post) === 'finished' && setting(look, 'finished') === 'line'

// highlighted reports a post that gets the gold mark: one addressed to root or
// to the room owner, by someone else. "Root only" marks root's own posts and
// the posts addressed to root; "Everyone to you" marks every post addressed to
// either. A post carries forRoot when its recipients include root's session, so
// a post to root keeps the mark wherever it shows.
export function highlighted(look: Look, post: FeedPost, roomName: string) {
  const mode = setting(look, 'highlight')
  if (mode === 'off' || post.owner || post.kind === 'pr') return false
  const toYou = post.forRoot === true || (post.to || []).includes(roomName)
  if (mode === 'all') return toYou
  return post.forRoot === true || (toYou && fromRoot(post))
}

// withChoice returns look with one choice set. A null value clears it, so the
// built-in default applies again.
export function withChoice(look: Look, id: ChoiceID, value: ChoiceValue): Look {
  const all = { ...look.all }
  if (value === null) delete all[id]
  else all[id] = value
  return { ...look, all }
}
