// Use the actual CSS animation, including its reduced-motion duration.
interface OpeningElement {
  getAnimations(): ReadonlyArray<{ finished: Promise<unknown> }>
}

export function afterOpen(node: OpeningElement, ready: () => void) {
  let active = true
  Promise.allSettled(node.getAnimations().map((animation) => animation.finished))
    .then(() => { if (active) ready() })
  return { destroy() { active = false } }
}
