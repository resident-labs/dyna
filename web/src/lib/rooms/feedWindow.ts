import { trimHistoryRows } from '../ui/historyWindow.ts'

export const FEED_WINDOW = 300

// The fields a feed post needs for the window helpers. Other fields, such as
// text and unread, pass through untouched.
export interface FeedPost {
  id: string
  views?: string[]
}

// The part of a room feed that the window helpers read. The feed's other
// fields, such as offset and counts, pass through.
export interface FeedPage<P extends FeedPost = FeedPost> {
  room: { id: string }
  posts?: P[] | null
  earlier?: number
  newer?: number
  cursor?: string
  historyLoaded?: boolean
  unread?: number
  viewUnread?: Record<string, number> | null
}

// Loading old pages releases far newer posts; following present releases old
// posts. The missing side is fetched again with the retained boundary ID.
export function trimFeed<F extends FeedPage>(feed: F, keep?: number, older?: boolean): F
export function trimFeed(feed: null, keep?: number, older?: boolean): null
export function trimFeed<F extends FeedPage>(feed: F | null, keep?: number, older?: boolean): F | null
export function trimFeed<F extends FeedPage>(feed: F | null, keep = FEED_WINDOW, older = false): F | null {
  const { rows, before, after } = trimHistoryRows(feed?.posts ?? [], keep, older)
  if (!feed || (!before && !after)) return feed
  const first = rows[0]
  if (!first) throw new Error('trimFeed keeps at least one post')
  return Object.assign({}, feed, {
    posts: rows,
    earlier: (feed.earlier ?? 0) + before,
    newer: (feed.newer ?? 0) + after,
    cursor: first.id,
    historyLoaded: true,
  })
}
