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
  completion?: boolean
  owner?: boolean
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

// kindOf names the choice that controls a post, or '' for a post that always shows.
export function kindOf(post: FeedPost): ChoiceID | '' {
  if (post.kind === 'pr') return 'prs'
  if (post.kind === 'goal') return 'goals'
  if (post.completion && post.kind !== 'failed' && !fromRoot(post)) return 'finished'
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

// highlighted reports a post that gets the gold mark: one addressed to the
// owner by someone else, limited to root's posts in "Root only".
export function highlighted(look: Look, post: FeedPost, roomName: string) {
  const mode = setting(look, 'highlight')
  if (mode === 'off' || post.owner || post.kind === 'pr' || !(post.to || []).includes(roomName)) return false
  return mode === 'all' || fromRoot(post)
}

// withChoice returns look with one choice set. A null value clears it, so the
// built-in default applies again.
export function withChoice(look: Look, id: ChoiceID, value: ChoiceValue): Look {
  const all = { ...look.all }
  if (value === null) delete all[id]
  else all[id] = value
  return { ...look, all }
}
