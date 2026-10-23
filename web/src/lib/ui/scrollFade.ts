// A sideways strip shows a soft fade at the edge where more content waits, so
// a reader on a phone sees that the tabs continue past the screen. The action
// sets data-scroll-fade on the scroller; app.css draws the fade from it.

export interface ScrollBox {
  scrollLeft: number
  scrollWidth: number
  clientWidth: number
}

export interface FadeSides {
  start: boolean
  end: boolean
}

/** One pixel of slack keeps a rounded scroll offset from flashing a fade. */
const SLACK = 1

/** fadeSides names the edges of a scroller that hide content. */
export function fadeSides(box: ScrollBox): FadeSides {
  const furthest = box.scrollWidth - box.clientWidth
  if (furthest <= SLACK) return { start: false, end: false }
  return { start: box.scrollLeft > SLACK, end: box.scrollLeft < furthest - SLACK }
}

/** scrollFade marks the edges of a sideways scroller that hide content. */
export function scrollFade(node: HTMLElement) {
  const mark = (): void => {
    const sides = fadeSides(node)
    const value = sides.start && sides.end ? 'both' : sides.end ? 'end' : sides.start ? 'start' : 'none'
    if (node.dataset.scrollFade !== value) node.dataset.scrollFade = value
  }
  // A tab count or a label changes the strip's width and a window resize
  // changes the room it has, so both move an edge without a scroll.
  const changed = new MutationObserver(mark)
  changed.observe(node, { childList: true, subtree: true, characterData: true })
  window.addEventListener('resize', mark)
  node.addEventListener('scroll', mark, { passive: true })
  mark()
  return {
    destroy() {
      changed.disconnect()
      window.removeEventListener('resize', mark)
      node.removeEventListener('scroll', mark)
    },
  }
}
