import { trimHistoryRows } from '../ui/historyWindow.js'

export const FEED_WINDOW = 300

// Loading old pages releases far newer posts; following present releases old
// posts. The missing side is fetched again with the retained boundary ID.
export function trimFeed(feed, keep = FEED_WINDOW, older = false) {
 const { rows: posts, before, after } = trimHistoryRows(feed?.posts ?? [], keep, older)
 if (!before && !after) return feed
 return { ...feed, posts, earlier: (feed.earlier ?? 0) + before,
  newer: (feed.newer ?? 0) + after, cursor: posts[0].id, historyLoaded: true }
}
