// Helpers that typeset JSON results for people (see JsonView.svelte). Key
// order is the author's: JSON.parse keeps it for named keys.

const ACRONYMS = new Set(['url', 'id', 'api', 'pr', 'ci', 'json', 'html'])

// fieldLabel turns a JSON key into words: agents_supported → Agents supported.
export function fieldLabel(key) {
  const words = String(key).replace(/([a-z0-9])([A-Z])/g, '$1 $2').split(/[_\s-]+/).filter(Boolean).map((w) => w.toLowerCase())
  return words.map((w, i) => (ACRONYMS.has(w) ? w.toUpperCase() : i === 0 ? w[0].toUpperCase() + w.slice(1) : w)).join(' ')
}

const isObject = (v) => v !== null && typeof v === 'object' && !Array.isArray(v)

// Fields a link-only object may have; anything else keeps it an object, so
// a record with a url never loses its other fields.
const LINK_KEYS = new Set(['url', 'title', 'name', 'text', 'label'])

// linkOf returns {text, url, host} for an http(s) URL string or a link-only
// object ({title, url}), else null. Other URL schemes never become links.
export function linkOf(v) {
  const linkOnly = isObject(v) && typeof v.url === 'string' && Object.keys(v).every((k) => LINK_KEYS.has(k))
  const url = typeof v === 'string' ? v : linkOnly ? v.url : ''
  if (!/^https?:\/\/[^\s]+$/.test(url)) return null
  let host = ''
  try { host = new URL(url).host } catch { return null }
  const text = isObject(v) ? v.title || v.name || url : url
  return { text, url, host }
}

// kindOf names how a value renders: text, scalar, empty, link, records (a
// list of objects), list, or object.
export function kindOf(v) {
  if (v === null || v === undefined || v === '') return 'empty'
  if (linkOf(v)) return 'link'
  if (typeof v === 'string') return 'text'
  if (Array.isArray(v)) return v.length && v.every((x) => isObject(x) && !linkOf(x)) ? 'records' : 'list'
  if (isObject(v)) return 'object'
  return 'scalar'
}

const TITLE_KEYS = ['title', 'name', 'tool', 'label', 'id']
const LINE_KEYS = ['summary', 'description']

// recordTitle is a record's short name from its own fields, else "Item n".
export function recordTitle(r, i = 0) {
  for (const k of TITLE_KEYS) if (typeof r?.[k] === 'string' && r[k].trim() && r[k].length <= 80) return r[k]
  return `Item ${i + 1}`
}

// recordLine is a record's one-line summary: summary or description, else
// its first string field after the title.
export function recordLine(r) {
  for (const k of LINE_KEYS) if (typeof r?.[k] === 'string' && r[k].trim()) return r[k]
  const title = recordTitle(r)
  for (const v of Object.values(r ?? {})) if (typeof v === 'string' && v.trim() && v !== title) return v
  return ''
}

// paragraphs splits text on blank lines.
export const paragraphs = (s) => String(s).split(/\n\s*\n/).map((p) => p.trim()).filter(Boolean)
