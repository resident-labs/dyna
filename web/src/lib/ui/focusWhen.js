// These actions focus the node while `on` is true, including each time a
// virtual list draws the node again. They wait one frame, so a closed
// details element around the node has opened by then.
const when = (scroll) => (node, on) => {
  const apply = (on) => on && requestAnimationFrame(() => node.isConnected && node.focus({ preventScroll: !scroll }))
  apply(on)
  return { update: apply }
}

// focusWhen does not scroll: the list already scrolled to the row.
export const focusWhen = when(false)
// revealWhen scrolls the node into view, for a part of a row that can be
// taller than the screen. A scroll by the page ends the list's own scroll.
export const revealWhen = when(true)
