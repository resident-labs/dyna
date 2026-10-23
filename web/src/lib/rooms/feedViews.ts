import { ancestors } from './memberOrder.ts'

// Feed views filter the existing room feed. They never change delivery or wakes.
export const GENERAL = 'general'
// ROOM_CHAT is how the UI names the room's main feed, the GENERAL view.
export const ROOM_CHAT = 'Room chat'
// feedView is the view of one lead's team. The token keeps its "channel:"
// prefix because routes and saved views store it, so a rename would break links
// the owner already has.
// TODO: legacy/shim remove later — change the token only together with a read
// for the "channel:" form.
export const feedView = (id: string | undefined): string => `channel:${id}`
// VOICE is the view the server routes voice call posts to
// (roomsvc.VoiceChannel). It has no row here; Voice's call widget shows it.
export const VOICE = 'voice-call'
export const isFeedView = (view: string): boolean => view === GENERAL || view.startsWith('channel:')

// The fields feed view routing reads from a member. Other member fields pass through.
export interface FeedMember {
  id?: string
  name: string
  parent?: string
  owner?: boolean
  role?: string
  closed?: boolean
}

// The fields feed view routing reads from a feed post.
export interface RoutedPost {
  views?: string[]
  conversation?: string
}

// inRoomFeed reports whether a post belongs in the room feed at all. A post
// written in a DM lives in that DM, so the room feed never lists it
// (internal/roomsvc/roomFeedPost). A live post still arrives for the DM, which
// reads it there.
export const inRoomFeed = (post: Pick<RoutedPost, 'conversation'>): boolean => !(post.conversation ?? '').startsWith('dm:')

// The server routes each room feed post into its feed views, the sender's home
// first (internal/roomsvc/feedviews.go). A post without them shows in the room chat.
export const inView = (post: RoutedPost, view: string): boolean => (post.views ?? [GENERAL]).includes(view)
export const homeView = (post: RoutedPost): string => post.views?.[0] ?? GENERAL

// What the shared draft parser reports about a draft.
export interface DraftParse {
  default?: boolean
  spawn?: unknown
}

export function feedModel(members: readonly FeedMember[] = []) {
  const agents = members.filter((m) => !m.owner)
  const byId = new Map<string | undefined, FeedMember>(agents.map((m): [string | undefined, FeedMember] => [m.id, m]))
  // A member's parent is its parent's session ID.
  const parent = (m?: FeedMember): FeedMember | undefined => byId.get(m?.parent)
  const home = (m: FeedMember): string => {
    const p = parent(m)
    return p ? feedView(p.id) : GENERAL
  }
  // Use stored relationships, including closed children whose history remains.
  const parentIds = new Set(agents.map((m) => parent(m)?.id).filter(Boolean))
  const leads = agents.filter((m) => parentIds.has(m.id))

  function resolveView(view: string): string {
    if (!view.startsWith('channel:')) return view
    // A saved channel link now opens the room chat.
    return GENERAL
  }

  const trail = (id?: string): FeedMember[] => ancestors(byId, id)

  return { agents, leads, byId, parent, home, trail, resolveView }
}

export type FeedModel = ReturnType<typeof feedModel>

// parentTrail is the breadcrumb above a session or run: the room's root, then
// each session from the top level down to id. A root session already heads it.
export function parentTrail(model: FeedModel, id: string | undefined, root: FeedMember): FeedMember[] {
  const trail = model.trail(id)
  return trail[0]?.role === 'root' ? trail : [root, ...trail]
}

// Use explicit names so preview and send resolve exactly the same recipients.
export async function prepareFeedText(
  text: string,
  lead: FeedMember | null,
  model: FeedModel,
  parseDraft: (text: string) => Promise<DraftParse>,
): Promise<string> {
  if (!lead) return text
  const parsed = await parseDraft(text)
  if (parsed.spawn || !parsed.default) return text
  const children = model.agents.filter((m) => !m.closed && model.parent(m)?.id === lead.id)
  const names = children.length ? children.map((m) => m.name) : [lead.name]
  return `${names.map((name) => '@' + name).join(' ')} ${text}`
}
