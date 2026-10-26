// A phone's browser bar, its keyboard and page zoom all shrink the part of the
// window a person can see without changing the layout viewport, and 100dvh
// follows neither of them on every browser. An app shell pinned to the layout
// viewport then ends below the visible area, so the end of a long page sits out
// of reach with nothing left to scroll.
//
// --app-height carries the height the window really shows, and the shell reads
// it. Every scroller inside the shell then ends where the screen does.

/** The parts of a window this module reads. A test supplies its own. */
export interface ViewportHost {
  innerHeight: number
  visualViewport: {
    height: number
    addEventListener: (name: 'resize', run: () => void) => void
    removeEventListener: (name: 'resize', run: () => void) => void
  } | null
  document: { documentElement: { style: { setProperty: (name: string, value: string) => void } } }
  addEventListener: (name: 'resize' | 'orientationchange', run: () => void) => void
  removeEventListener: (name: 'resize' | 'orientationchange', run: () => void) => void
}

export function trackViewportHeight(win: ViewportHost = window): () => void {
  const root = win.document.documentElement
  const viewport = win.visualViewport
  const apply = () => {
    const height = viewport?.height ?? win.innerHeight
    if (height > 0) root.style.setProperty('--app-height', `${Math.round(height)}px`)
  }
  apply()
  viewport?.addEventListener('resize', apply)
  win.addEventListener('resize', apply)
  win.addEventListener('orientationchange', apply)
  return () => {
    viewport?.removeEventListener('resize', apply)
    win.removeEventListener('resize', apply)
    win.removeEventListener('orientationchange', apply)
  }
}
