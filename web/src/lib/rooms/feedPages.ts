import { trimFeed } from './feedWindow.ts'
import type { FeedPage, FeedPost } from './feedWindow.ts'

// How an incoming page joins the shown feed: true or 'older' prepends it,
// 'newer' appends it, 'present' replaces it, and any other value refreshes
// the overlapping pages.
export type PageDirection = boolean | string

// Explicit page reads extend one end. Background tail refreshes update only
// overlapping records while a newer page is missing.
export function mergeFeedPage<F extends FeedPage>(current: F | null, page: F, direction: PageDirection = false): F {
 if (!current || current.room.id !== page.room.id || direction === 'present') return page
 const older = direction === true || direction === 'older'
 const currentPosts = current.posts ?? []
 const mine = new Map<string, FeedPost>(currentPosts.map((post): [string, FeedPost] => [post.id, post]))
 // Loaded posts keep their object, but take the server's views: a promote
 // moves posts to another feed view.
 const incoming = (page.posts ?? []).map((post) => {
  const have = mine.get(post.id)
  if (!have) return post
  return String(have.views) === String(post.views) ? have : { ...have, views: post.views }
 })
 const have = new Set(incoming.map((post) => post.id))
 if (older) return trimFeed(Object.assign({}, current, { earlier: page.earlier ?? 0,
  cursor: page.cursor ?? '', historyLoaded: true,
  posts: [...incoming, ...currentPosts.filter((post) => !have.has(post.id))] }), undefined, true)
 if (direction === 'newer') return trimFeed(Object.assign({}, current, { newer: page.newer ?? 0, historyLoaded: true,
  posts: [...currentPosts, ...incoming.filter((post) => !mine.has(post.id))] }))
 if (current.newer) return Object.assign({}, page, current, {
  viewUnread: page.viewUnread ?? current.viewUnread,
  unread: page.unread ?? current.unread })
 if (!current.historyLoaded) return page
 const first = currentPosts.findIndex((post) => have.has(post.id))
 if (first < 0) return Object.assign({}, current, { newer: Math.max(1, (page.earlier ?? 0) + incoming.length - (current.earlier ?? 0) - currentPosts.length) })
 let last = currentPosts.length - 1
 while (!have.has(currentPosts[last]?.id ?? '')) last--
 return trimFeed(Object.assign({}, page, { earlier: current.earlier, cursor: current.cursor, historyLoaded: true,
  posts: [...currentPosts.slice(0,first), ...incoming, ...currentPosts.slice(last+1)] }))
}

// rerouteAnchors names the loaded posts to read around (at most 200 posts per
// page) so the pages cover every loaded post.
export function rerouteAnchors(posts: readonly FeedPost[] | null | undefined, page = 200): string[] {
 const list = posts ?? []
 const out: string[] = []
 for (let i = 0; i < list.length; i += page - 10) {
  const anchor = list[Math.min(i + page / 2 - 5, list.length - 1)]
  if (anchor) out.push(anchor.id)
 }
 return out
}

// The part of a page the feed view refresh reads: its posts and their views.
export interface ViewPage<P extends FeedPost> {
 posts?: P[] | null
}

// withViews gives loaded posts the views in fresh pages. A post whose views
// did not change keeps its object.
export function withViews<P extends FeedPost>(posts: P[], pages: ViewPage<P>[]): P[] {
 const fresh = new Map<string, string[] | undefined>(pages.flatMap((page) => page.posts ?? []).map((post): [string, string[] | undefined] => [post.id, post.views]))
 return posts.map((post) => !fresh.has(post.id) || String(fresh.get(post.id)) === String(post.views) ? post : Object.assign({}, post, { views: fresh.get(post.id) }))
}
