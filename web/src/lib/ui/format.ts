import { marked } from 'marked'
import DOMPurify from 'dompurify'
import type { Room } from '../api/room.ts'

marked.setOptions({ gfm: true, breaks: true })

const MENTION = /(^|\s)@[A-Za-z0-9][\w.:-]*/
const MENTION_SPLIT = /((?:^|(?<=\s))@[A-Za-z0-9][\w.:-]*)/

// mentionize wraps @tokens outside code in a span so they read as mentions.
function mentionize(html: string): string {
  const doc = new DOMParser().parseFromString(`<div>${html}</div>`, 'text/html')
  const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT)
  const hits: Text[] = []
  while (walker.nextNode()) {
    const n = walker.currentNode as Text
    if (n.parentElement?.closest('code, pre, a')) continue
    if (MENTION.test(n.data)) hits.push(n)
  }
  for (const n of hits) {
    const frag = doc.createDocumentFragment()
    for (const part of n.data.split(MENTION_SPLIT)) {
      if (part.startsWith('@')) {
        const s = doc.createElement('span')
        s.className = 'mention'
        s.textContent = part
        frag.appendChild(s)
      } else if (part) frag.appendChild(doc.createTextNode(part))
    }
    n.replaceWith(frag)
  }
  // The parser always makes the wrapper div first, so it is the first element.
  const wrapper = doc.body.firstChild as HTMLElement
  return wrapper.innerHTML
}

// turnText drops a completion's "<addr> turn N completed. Result:" lead-in;
// the row that shows it already says so.
const turnLead = /^\S+ turn \d+ (completed|failed|stopped)\.\s*(Result:\s*)?/
export function turnText(s: string | null | undefined): string {
  return (s || '').replace(turnLead, '')
}

// resultLine is a completion's result on one line, for a folded feed row.
export const resultLine = (s: string | null | undefined): string => turnText(s).replace(/\s+/g, ' ').trim()

// Parsing, sanitizing and the mention pass cost about a millisecond per post,
// and the feed renders the same texts again on every view switch. The cache
// keeps the newest renders; its key is the text, the only input.
const rendered = new Map<string, string>()
const renderedMax = 2000

export function markdown(text: string | null | undefined): string {
  const src = text || ''
  let html = rendered.get(src)
  if (html === undefined) {
    html = mentionize(DOMPurify.sanitize(marked.parse(src, { async: false })))
    if (rendered.size >= renderedMax) {
      const oldest = rendered.keys().next().value
      if (oldest !== undefined) rendered.delete(oldest)
    }
    rendered.set(src, html)
  }
  return html
}

// internalLinkHash returns the app route in a same-origin link. Other paths,
// such as attachments, keep their normal external-link behavior.
export function internalLinkHash(href: string, current: Pick<Location, 'origin' | 'pathname'>): string | null {
  try {
    const url = new URL(href, current.origin)
    if (url.origin !== current.origin) return null
    if (url.pathname !== '/' && url.pathname !== current.pathname) return null
    return url.hash
  } catch {
    return null
  }
}

// followInternalLink uses native hash navigation so the router and every open
// item pane receive the same hashchange event.
export function followInternalLink(hash: string, loc: Pick<Location, 'hash'>): void {
  if (loc.hash !== hash) loc.hash = hash
}

const two = (n: number): string => String(n).padStart(2, '0')

export function clock(ts: string | number | Date = NaN): string {
  const d = new Date(ts)
  return `${two(d.getHours())}:${two(d.getMinutes())}`
}

export function stamp(ts: string | number | Date = NaN): string {
  const d = new Date(ts)
  const now = new Date()
  const day = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const then = new Date(d.getFullYear(), d.getMonth(), d.getDate())
  const diff = Math.round((day.getTime() - then.getTime()) / 86400000)
  if (diff === 0) return `Today at ${clock(ts)}`
  if (diff === 1) return `Yesterday at ${clock(ts)}`
  return `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} at ${clock(ts)}`
}

export function dayLabel(ts: string | number | Date = NaN): string {
  return new Date(ts).toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' })
}

export function ago(iso: string | number | Date = NaN): string {
  const t = new Date(iso).getTime()
  if (!t || t < 0) return ''
  const s = Math.max(0, (Date.now() - t) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

// until reads a future time the way ago reads a past one: how long from now.
export function until(iso: string | number | Date = NaN): string {
  const t = new Date(iso).getTime()
  if (!t || t < 0) return ''
  const s = Math.max(0, (t - Date.now()) / 1000)
  if (s < 60) return 'in under a minute'
  if (s < 3600) return `in ${Math.floor(s / 60)}m`
  if (s < 86400) return `in ${Math.floor(s / 3600)}h`
  return `in ${Math.floor(s / 86400)}d`
}

export function duration(a: string | number | Date = NaN, b?: string | number | Date | null): string {
  const start = new Date(a).getTime()
  const end = b && new Date(b).getTime() > 0 ? new Date(b).getTime() : Date.now()
  if (!start || start < 0) return ''
  const s = Math.round((end - start) / 1000)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m${two(s % 60)}s`
  return `${Math.floor(s / 3600)}h${two(Math.floor(s / 60) % 60)}m`
}

export function tokens(n: number | null | undefined): string {
  if (!n) return '0'
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`
  return String(n)
}

export function initials(name: string | null | undefined): string {
  const [first = '?', second] = (name || '?').split(/[-_.:\s]+/).filter(Boolean)
  if (second !== undefined) return (first.charAt(0) + second.charAt(0)).toUpperCase()
  return first.slice(0, 2).toUpperCase()
}

// short drops the room prefix from an address, and names the owner "root".
// Only a post the human typed is "you" (post.speaker from Go).
export function short(addr: string | undefined, roomName?: string): string {
  if (addr === roomName) return 'root'
  if (addr === undefined) throw new TypeError('A sender address is required.')
  if (!addr.startsWith(roomName + ':')) return addr
  if (roomName === undefined) throw new TypeError("Cannot read properties of undefined (reading 'length')")
  return addr.slice(roomName.length + 1)
}

// roomLabel is the name to show for a room: the owner's display name, or
// the routing name that addresses use.
export function roomLabel(r: Partial<Pick<Room, 'displayName' | 'name'>> | null | undefined): string {
  return r?.displayName || r?.name || ''
}

// elapsed reads a running call's time like a stopwatch: 8s, 1m 05s, 1h 02m.
export function elapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`
}

// isLongCall marks a tool call or compaction that has run over 30 seconds.
export const isLongCall = (ms: number): boolean => ms > 30000
