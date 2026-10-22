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
  channels: Record<string, Values>
}
interface FeedPost {
  id?: string
  speaker?: string
  kind?: string
  completion?: boolean
  owner?: boolean
  to?: string[]
}

// How a channel's feed looks. The owner sets defaults for every channel in
// Settings and can override them for one channel from its top-bar menu.
// A choice with options picks one of them; one without is on or off.
export const CHOICES: Choice[] = [
  { id: 'highlight', label: 'Highlight', hint: 'Posts that get the gold mark', options: [['root', 'Root only'], ['all', 'Everyone to you'], ['off', 'Off']] },
  { id: 'finished', label: 'Finished turns', hint: 'Root’s results always show in full. Failures always show.', options: [['full', 'Full'], ['line', 'One line'], ['hidden', 'Hidden']] },
  { id: 'prs', label: 'Pull request updates', hint: 'Opened, reviewed, checked, merged' },
  { id: 'goals', label: 'Goal updates', hint: 'Goals added, done or dropped' },
]

const DEFAULTS: Record<ChoiceID, string | boolean> = { highlight: 'root', finished: 'line', prs: true, goals: true }

// setting resolves one choice: the scope's own value, else the default for
// every channel, else the built-in default.
export function setting(look: Look, scope: string | null, id: ChoiceID) {
  const own = scope === null ? undefined : look.channels?.[scope]?.[id]
  return own ?? look.all?.[id] ?? DEFAULTS[id]
}

// differs lists the choices where a channel does not match its default.
export function differs(look: Look, scope: string) {
  return CHOICES.filter((c) => (look.channels?.[scope]?.[c.id] ?? null) !== null && setting(look, scope, c.id) !== setting(look, null, c.id)).map((c) => c.id)
}

// scopeOf keys a channel's own choices. Every room has a general channel, so
// the room id is part of the key.
export const scopeOf = (roomId: string, view: string) => `${roomId}:${view}`

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
export function visibleTo(look: Look, scope: string, revealed: Record<string, boolean> = {}) {
  return (post: FeedPost) => {
    const k = kindOf(post)
    if (!k || (post.id !== undefined && revealed[post.id] === true)) return true
    const v = setting(look, scope, k)
    return k === 'finished' ? v !== 'hidden' : v
  }
}

// oneLine reports a finished turn the feed shows as a single line.
export const oneLine = (look: Look, scope: string, post: FeedPost) => kindOf(post) === 'finished' && setting(look, scope, 'finished') === 'line'

// highlighted reports a post that gets the gold mark: one addressed to the
// owner by someone else, limited to root's posts in "Root only".
export function highlighted(look: Look, scope: string, post: FeedPost, roomName: string) {
  const mode = setting(look, scope, 'highlight')
  if (mode === 'off' || post.owner || post.kind === 'pr' || !(post.to || []).includes(roomName)) return false
  return mode === 'all' || fromRoot(post)
}

// withChoice returns look with one choice set. A null value clears a scope's
// override so it follows the default again.
export function withChoice(look: Look, scope: string | null, id: ChoiceID, value: ChoiceValue): Look {
  if (scope === null) return { ...look, all: { ...look.all, [id]: value } }
  const own = { ...look.channels?.[scope] }
  if (value === null) delete own[id]
  else own[id] = value
  const channels = { ...look.channels }
  if (Object.keys(own).length) channels[scope] = own
  else delete channels[scope]
  return { ...look, channels }
}
