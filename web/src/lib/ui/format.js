import { marked } from 'marked'
import DOMPurify from 'dompurify'

marked.setOptions({ gfm: true, breaks: true })

// mentionize wraps @tokens outside code in a span so they read as mentions.
function mentionize(html) {
  const doc = new DOMParser().parseFromString(`<div>${html}</div>`, 'text/html')
  const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_TEXT)
  const hits = []
  while (walker.nextNode()) {
    const n = walker.currentNode
    if (n.parentElement.closest('code, pre, a')) continue
    if (/(^|\s)@[A-Za-z0-9][\w.:-]*/.test(n.nodeValue)) hits.push(n)
  }
  for (const n of hits) {
    const frag = doc.createDocumentFragment()
    const parts = n.nodeValue.split(/((?:^|(?<=\s))@[A-Za-z0-9][\w.:-]*)/)
    for (const part of parts) {
      if (part.startsWith('@')) {
        const s = doc.createElement('span')
        s.className = 'mention'
        s.textContent = part
        frag.appendChild(s)
      } else if (part) frag.appendChild(doc.createTextNode(part))
    }
    n.replaceWith(frag)
  }
  return doc.body.firstChild.innerHTML
}

// turnText drops a completion's "<addr> turn N completed. Result:" lead-in;
// the row that shows it already says so.
const turnLead = /^\S+ turn \d+ (completed|failed|stopped)\.\s*(Result:\s*)?/
export function turnText(s) {
  return (s || '').replace(turnLead, '')
}

// resultLine is a completion's result on one line, for a folded feed row.
export const resultLine = (s) => turnText(s).replace(/\s+/g, ' ').trim()

// Parsing, sanitizing and the mention pass cost about a millisecond per post,
// and the feed renders the same texts again on every view switch. The cache
// keeps the newest renders; its key is the text, the only input.
const rendered = new Map()
const renderedMax = 2000

export function markdown(text) {
  text = text || ''
  let html = rendered.get(text)
  if (html === undefined) {
    html = mentionize(DOMPurify.sanitize(marked.parse(text)))
    if (rendered.size >= renderedMax) rendered.delete(rendered.keys().next().value)
    rendered.set(text, html)
  }
  return html
}

const two = (n) => String(n).padStart(2, '0')

export function clock(ts) {
  const d = new Date(ts)
  return `${two(d.getHours())}:${two(d.getMinutes())}`
}

export function stamp(ts) {
  const d = new Date(ts)
  const now = new Date()
  const day = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const then = new Date(d.getFullYear(), d.getMonth(), d.getDate())
  const diff = Math.round((day - then) / 86400000)
  if (diff === 0) return `Today at ${clock(ts)}`
  if (diff === 1) return `Yesterday at ${clock(ts)}`
  return `${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} at ${clock(ts)}`
}

export function dayLabel(ts) {
  return new Date(ts).toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' })
}

export function ago(iso) {
  const t = new Date(iso).getTime()
  if (!t || t < 0) return ''
  const s = Math.max(0, (Date.now() - t) / 1000)
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
}

export function duration(a, b) {
  const start = new Date(a).getTime()
  const end = b && new Date(b).getTime() > 0 ? new Date(b).getTime() : Date.now()
  if (!start || start < 0) return ''
  const s = Math.round((end - start) / 1000)
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m${two(s % 60)}s`
  return `${Math.floor(s / 3600)}h${two(Math.floor(s / 60) % 60)}m`
}

export function tokens(n) {
  if (!n) return '0'
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`
  return String(n)
}

export function initials(name) {
  const parts = (name || '?').split(/[-_.:\s]+/).filter(Boolean)
  if (parts.length >= 2) return (parts[0][0] + parts[1][0]).toUpperCase()
  return (parts[0] || '?').slice(0, 2).toUpperCase()
}

// short drops the room prefix from an address, and names the owner "root".
// Only a post the human typed is "you" (post.speaker from Go).
export function short(addr, roomName) {
  if (addr === roomName) return 'root'
  return addr.startsWith(roomName + ':') ? addr.slice(roomName.length + 1) : addr
}

// roomLabel is the name to show for a room: the owner's display name, or
// the routing name that addresses use.
export function roomLabel(r) {
  return r?.displayName || r?.name || ''
}

// elapsed reads a running call's time like a stopwatch: 8s, 1m 05s, 1h 02m.
export function elapsed(ms) {
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`
}

// isLongCall marks a tool call or compaction that has run over 30 seconds.
export const isLongCall = (ms) => ms > 30000
