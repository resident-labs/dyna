// How code reads in the file viewer and the diffs, shared by both and kept
// in localStorage. Long lines wrap by default: sideways scrolling is hard
// on a phone and slow on a desktop.
const KEY = 'dyna.code'

function load() {
  try {
    return JSON.parse(localStorage.getItem(KEY) || '{}')
  } catch {
    return {}
  }
}

export const code = $state({ wrap: load().wrap ?? true })

export function setWrap(on) {
  code.wrap = on
  try {
    localStorage.setItem(KEY, JSON.stringify({ wrap: on }))
  } catch {}
}
