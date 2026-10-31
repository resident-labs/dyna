// The ends of a scrolling list to page at. moved is how far scrollTop changed in a scroll event:
// the list also scrolls itself (following a live row, keeping its anchor when rows load above),
// so only a move toward an end pages that end. A check after a change of the rows has no move;
// it pages only when the rows overflow the view by more than the edge. Otherwise each change
// would page older, release the newest end, and walk the feed away from the present.
export function shouldPage(top: number, height: number, client: number, edge: number, moved?: number): { start: boolean; end: boolean } {
  const start = top < edge, end = height - top - client < edge;
  if (moved !== undefined) return { start: start && moved < 0, end: end && moved > 0 };
  if (height <= client + edge) return { start: false, end: false };
  return { start, end };
}
