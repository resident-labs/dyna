// How code reads in the file viewer and the diffs, shared by both and kept
// in localStorage. Long lines wrap by default: sideways scrolling is hard
// on a phone and slow on a desktop.
const KEY = 'dyna.code'

function load() {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(KEY) || '{}')
    return value && typeof value === 'object' && 'wrap' in value && typeof value.wrap === 'boolean' ? { wrap: value.wrap } : {}
  } catch {
    return {}
  }
}

export const code = $state({ wrap: load().wrap ?? true })

export function setWrap(on: boolean) {
  code.wrap = on
  try {
    localStorage.setItem(KEY, JSON.stringify({ wrap: on }))
  } catch {}
}
