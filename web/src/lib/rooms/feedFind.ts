// Text rules for find-in-feed. The server finds the posts (roomsvc.SearchFeed);
// these functions only cut the text a result row shows.

export interface Part {
  text: string
  hit: boolean
}

// snippet cuts the post text to about width characters, starting just
// before the first matched word, on one line, and marks every match of the query words.
export function snippet(text: string, query: string, width = 140): Part[] {
  const flat = text.replace(/\s+/g, ' ').trim()
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  const lower = flat.toLowerCase()
  // A post that matched only on its sender shows from its start.
  const found = words.map((w) => lower.indexOf(w)).filter((i) => i >= 0)
  const first = found.length ? Math.min(...found) : 0
  // A short lead keeps the match on screen when a phone shows one short line.
  const lead = Math.min(Math.floor(width / 3), 20)
  let start = first > lead ? first - lead : 0
  const end = Math.min(flat.length, start + width)
  start = Math.max(0, Math.min(start, end - width))
  const cut = (start > 0 ? '…' : '') + flat.slice(start, end) + (end < flat.length ? '…' : '')
  return marks(cut, words)
}

// marks finds the words in the lowercased text and marks the same places in
// the text. A character whose lowercase form has another length (such as
// "İ") would shift the places, so such text gets no marks.
function marks(text: string, words: string[]): Part[] {
  const lower = text.toLowerCase()
  if (lower.length !== text.length) return [{ text, hit: false }]
  const hit = new Array<boolean>(text.length).fill(false)
  for (const w of words) {
    for (let i = lower.indexOf(w); i >= 0; i = lower.indexOf(w, i + 1)) hit.fill(true, i, i + w.length)
  }
  const parts: Part[] = []
  for (let i = 0; i < text.length; i++) {
    const ch = text[i] ?? ''
    const on = hit[i] ?? false
    const last = parts[parts.length - 1]
    if (last && last.hit === on) last.text += ch
    else parts.push({ text: ch, hit: on })
  }
  return parts
}
