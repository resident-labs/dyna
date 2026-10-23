// Shared motion timings. CSS uses the matching tokens in app.css
// (--motion-fast, --motion, --motion-slow); Svelte transitions use these.
export const FAST = 150 // hover, focus, small state changes
export const BASE = 200 // items and panels entering or leaving
export const SLOW = 250 // whole views

// motion returns a transition duration, or 0 when the user asks for reduced motion.
export function motion(ms: number) {
  return typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches ? 0 : ms
}

// arrivals returns a function that, for a list's scope (a room or session ID)
// and its current row keys, gives a test for rows that arrived below rows the
// list already showed. Only those rows animate in: a newly opened list and
// earlier rows loaded above stay still. It remembers every key it has seen, so
// the boundary survives a list that trims its oldest rows.
export function arrivals() {
  let scope: string | undefined, seen: Set<string> | null = null, fresh = new Set<string>()
  return (id: string | undefined, keys: readonly string[]) => {
    if (id !== scope) { scope = id; seen = null; fresh = new Set<string>() }
    if (!seen) {
      if (keys.length) seen = new Set(keys)
      return () => false
    }
    const previous = seen
    let last = -1
    keys.forEach((k, i) => { if (previous.has(k)) last = i })
    // With no row in common, the list was replaced: nothing counts as arrived.
    if (last >= 0) for (const k of keys.slice(last + 1)) fresh.add(k)
    for (const k of keys) seen.add(k)
    return (key: string) => fresh.has(key)
  }
}
