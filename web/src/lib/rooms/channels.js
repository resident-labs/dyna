import { ancestors } from './memberOrder.js'

// Channels group the existing room feed. They never change delivery or wakes.
export const GENERAL = 'general'
export const channelView = (id) => `channel:${id}`
// VOICE is the channel the server routes voice call posts to
// (roomsvc.VoiceChannel). It has no view here; Voice's call widget shows it.
export const VOICE = 'voice-call'
export const isChannel = (view) => view === GENERAL || view.startsWith('channel:')

// The server routes each room feed post into its channels, the sender's home
// first (internal/roomsvc/channels.go). A post without them shows in general.
export const inChannel = (post, view) => (post.channels ?? [GENERAL]).includes(view)
export const homeChannel = (post) => post.channels?.[0] ?? GENERAL

export function channelModel(members = []) {
  const agents = members.filter((m) => !m.owner)
  const byId = new Map(agents.map((m) => [m.id, m]))
  // A member's parent is its parent's session ID.
  const parent = (m) => byId.get(m?.parent)
  const home = (m) => parent(m) ? channelView(parent(m).id) : GENERAL
  // Use stored relationships, including closed children whose history remains.
  const parentIds = new Set(agents.map((m) => parent(m)?.id).filter(Boolean))
  const leads = agents.filter((m) => parentIds.has(m.id))

  function resolveView(view) {
    if (!view.startsWith('channel:')) return view
    // Old channel links now open the room's Messages tab.
    return GENERAL
  }

  const trail = (id) => ancestors(byId, id)

  return { agents, leads, byId, parent, home, trail, resolveView }
}

// parentTrail is the breadcrumb above a session or run: the room's root, then
// each session from the top level down to id. A root session already heads it.
export function parentTrail(model, id, root) {
  const trail = model.trail(id)
  return trail[0]?.role === 'root' ? trail : [root, ...trail]
}

// Use explicit names so preview and send resolve exactly the same recipients.
export async function prepareChannelText(text, channel, model, parseDraft) {
  if (!channel) return text
  const parsed = await parseDraft(text)
  if (parsed.spawn || !parsed.default) return text
  const children = model.agents.filter((m) => !m.closed && model.parent(m)?.id === channel.id)
  const names = children.length ? children.map((m) => m.name) : [channel.name]
  return `${names.map((name) => '@' + name).join(' ')} ${text}`
}
