import { trimFeed } from './feedWindow.js'

// Explicit page reads extend one end. Background tail refreshes update only
// overlapping records while a newer page is missing.
export function mergeFeedPage(current, page, direction = false) {
 if (!current || current.room.id !== page.room.id || direction === 'present') return page
 const older = direction === true || direction === 'older'
 const mine = new Map((current.posts ?? []).map(post => [post.id, post]))
 // Loaded posts keep their object, but take the server's channels: a promote
 // moves posts to another channel.
 const incoming = (page.posts ?? []).map(post => {
  const have = mine.get(post.id)
  if (!have) return post
  return String(have.channels) === String(post.channels) ? have : { ...have, channels: post.channels }
 })
 const have = new Set(incoming.map(post => post.id))
 if (older) return trimFeed({ ...current, earlier: page.earlier ?? 0,
  cursor: page.cursor ?? '', historyLoaded: true,
  posts: [...incoming, ...(current.posts ?? []).filter(post => !have.has(post.id))] }, undefined, true)
 if (direction === 'newer') return trimFeed({ ...current, newer: page.newer ?? 0, historyLoaded: true,
  posts: [...current.posts, ...incoming.filter(post => !mine.has(post.id))] })
 if (current.newer) return { ...page, ...current,
  channelUnread: page.channelUnread ?? current.channelUnread,
  unread: page.unread ?? current.unread }
 if (!current.historyLoaded) return page
 const first = current.posts.findIndex(post => have.has(post.id))
 if (first < 0) return { ...current, newer: Math.max(1, (page.earlier ?? 0) + incoming.length - (current.earlier ?? 0) - current.posts.length) }
 let last = current.posts.length - 1
 while (!have.has(current.posts[last].id)) last--
 return trimFeed({ ...page, earlier: current.earlier, cursor: current.cursor, historyLoaded: true,
  posts: [...current.posts.slice(0,first), ...incoming, ...current.posts.slice(last+1)] })
}

// rerouteAnchors names the loaded posts to read around (at most 200 posts per
// page) so the pages cover every loaded post.
export function rerouteAnchors(posts, page = 200) {
 const out = []
 for (let i = 0; i < posts.length; i += page - 10) out.push(posts[Math.min(i + page / 2 - 5, posts.length - 1)].id)
 return out
}

// withChannels gives loaded posts the channels in fresh pages. A post whose
// channels did not change keeps its object.
export function withChannels(posts, pages) {
 const fresh = new Map(pages.flatMap(page => page.posts ?? []).map(post => [post.id, post.channels]))
 return posts.map(post => !fresh.has(post.id) || String(fresh.get(post.id)) === String(post.channels) ? post : { ...post, channels: fresh.get(post.id) })
}
