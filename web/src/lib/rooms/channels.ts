import { ancestors } from './memberOrder.ts'

// Channels group the existing room feed. They never change delivery or wakes.
export const GENERAL = 'general'
export const channelView = (id: string | undefined): string => `channel:${id}`
// VOICE is the channel the server routes voice call posts to
// (roomsvc.VoiceChannel). It has no view here; Voice's call widget shows it.
export const VOICE = 'voice-call'
export const isChannel = (view: string): boolean => view === GENERAL || view.startsWith('channel:')

// The fields channels read from a member. Other member fields pass through.
export interface ChannelMember {
  id?: string
  name: string
  parent?: string
  owner?: boolean
  role?: string
  closed?: boolean
}

// The fields channel routing reads from a feed post.
export interface RoutedPost {
  channels?: string[]
}

// The server routes each room feed post into its channels, the sender's home
// first (internal/roomsvc/channels.go). A post without them shows in general.
export const inChannel = (post: RoutedPost, view: string): boolean => (post.channels ?? [GENERAL]).includes(view)
export const homeChannel = (post: RoutedPost): string => post.channels?.[0] ?? GENERAL

// What the shared draft parser reports about a draft.
export interface DraftParse {
  default?: boolean
  spawn?: unknown
}

export function channelModel(members: readonly ChannelMember[] = []) {
  const agents = members.filter((m) => !m.owner)
  const byId = new Map<string | undefined, ChannelMember>(agents.map((m): [string | undefined, ChannelMember] => [m.id, m]))
  // A member's parent is its parent's session ID.
  const parent = (m?: ChannelMember): ChannelMember | undefined => byId.get(m?.parent)
  const home = (m: ChannelMember): string => {
    const p = parent(m)
    return p ? channelView(p.id) : GENERAL
  }
  // Use stored relationships, including closed children whose history remains.
  const parentIds = new Set(agents.map((m) => parent(m)?.id).filter(Boolean))
  const leads = agents.filter((m) => parentIds.has(m.id))

  function resolveView(view: string): string {
    if (!view.startsWith('channel:')) return view
    // Old channel links now open the room's Messages tab.
    return GENERAL
  }

  const trail = (id?: string): ChannelMember[] => ancestors(byId, id)

  return { agents, leads, byId, parent, home, trail, resolveView }
}

export type ChannelModel = ReturnType<typeof channelModel>

// parentTrail is the breadcrumb above a session or run: the room's root, then
// each session from the top level down to id. A root session already heads it.
export function parentTrail(model: ChannelModel, id: string | undefined, root: ChannelMember): ChannelMember[] {
  const trail = model.trail(id)
  return trail[0]?.role === 'root' ? trail : [root, ...trail]
}

// Use explicit names so preview and send resolve exactly the same recipients.
export async function prepareChannelText(
  text: string,
  channel: ChannelMember | null,
  model: ChannelModel,
  parseDraft: (text: string) => Promise<DraftParse>,
): Promise<string> {
  if (!channel) return text
  const parsed = await parseDraft(text)
  if (parsed.spawn || !parsed.default) return text
  const children = model.agents.filter((m) => !m.closed && model.parent(m)?.id === channel.id)
  const names = children.length ? children.map((m) => m.name) : [channel.name]
  return `${names.map((name) => '@' + name).join(' ')} ${text}`
}
