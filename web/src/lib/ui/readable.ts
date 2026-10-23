// Helpers that typeset JSON results for people (see JsonView.svelte). Key
// order is the author's: JSON.parse keeps it for named keys.
import { isRecord, isString } from '../api/guards.ts'

const ACRONYMS = new Set(['url', 'id', 'api', 'pr', 'ci', 'json', 'html'])

// fieldLabel turns a JSON key into words: agents_supported → Agents supported.
export function fieldLabel(key: unknown): string {
  const words = String(key).replace(/([a-z0-9])([A-Z])/g, '$1 $2').split(/[_\s-]+/).filter(Boolean).map((w) => w.toLowerCase())
  return words.map((w, i) => (ACRONYMS.has(w) ? w.toUpperCase() : i === 0 ? w.charAt(0).toUpperCase() + w.slice(1) : w)).join(' ')
}

// Fields a link-only object may have; anything else keeps it an object, so
// a record with a url never loses its other fields.
const LINK_KEYS = new Set(['url', 'title', 'name', 'text', 'label'])

// A link's URL: the string itself, or the url of a link-only object. Other
// values have no URL.
function urlOf(v: unknown): string {
  if (typeof v === 'string') return v
  if (isRecord(v) && isString(v.url) && Object.keys(v).every((k) => LINK_KEYS.has(k))) return v.url
  return ''
}

export interface Link {
  // The title as written in the JSON, unchanged; a string only when the
  // link has no title.
  text: unknown
  url: string
  host: string
}

// linkOf returns {text, url, host} for an http(s) URL string or a link-only
// object ({title, url}), else null. Other URL schemes never become links.
export function linkOf(v: unknown): Link | null {
  const url = urlOf(v)
  if (!/^https?:\/\/[^\s]+$/.test(url)) return null
  let host = ''
  try { host = new URL(url).host } catch { return null }
  const text = isRecord(v) ? v.title || v.name || url : url
  return { text, url, host }
}

// isList narrows an unknown value to an array of unknown items.
const isList = (v: unknown): v is unknown[] => Array.isArray(v)

export type Kind = 'empty' | 'link' | 'text' | 'records' | 'list' | 'object' | 'scalar'

// kindOf names how a value renders: text, scalar, empty, link, records (a
// list of objects), list, or object.
export function kindOf(v: unknown): Kind {
  if (v === null || v === undefined || v === '') return 'empty'
  if (linkOf(v)) return 'link'
  if (typeof v === 'string') return 'text'
  if (isList(v)) return v.length > 0 && v.every((x) => isRecord(x) && !linkOf(x)) ? 'records' : 'list'
  if (isRecord(v)) return 'object'
  return 'scalar'
}

const TITLE_KEYS = ['title', 'name', 'tool', 'label', 'id']
const LINE_KEYS = ['summary', 'description']

// field reads one property of any value, as r?.[k] does: arrays and functions
// have properties too. Primitives are boxed first; null and undefined have none.
const field = (r: unknown, k: string): unknown => {
  if (r === null || r === undefined) return undefined
  const boxed: object = Object(r)
  return Reflect.get(boxed, k)
}

// recordTitle is a record's short name from its own fields, else "Item n".
export function recordTitle(r: unknown, i = 0): string {
  for (const k of TITLE_KEYS) {
    const s = field(r, k)
    if (isString(s) && s.trim() && s.length <= 80) return s
  }
  return `Item ${i + 1}`
}

// recordLine is a record's one-line summary: summary or description, else
// its first string field after the title.
export function recordLine(r: unknown): string {
  for (const k of LINE_KEYS) {
    const s = field(r, k)
    if (isString(s) && s.trim()) return s
  }
  const title = recordTitle(r)
  const values: unknown[] = Object.values(r ?? {})
  for (const v of values) if (isString(v) && v.trim() && v !== title) return v
  return ''
}

// paragraphs splits text on blank lines.
export const paragraphs = (s: unknown): string[] => String(s).split(/\n\s*\n/).map((p) => p.trim()).filter(Boolean)
