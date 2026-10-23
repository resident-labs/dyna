// Text rules for palette messages. The server finds the posts (roomsvc.SearchFeed);
// these functions only cut the text a result row shows.

export interface Part {
  text: string
  hit: boolean
}

// Goal IDs ("g317", "g317/i4") address work inside Dyna. An excerpt reads as
// a sentence, so they are left out; a PR number such as "#288" stays. A
// goal's item number goes too, so "Goal item i12 is done" reads "Goal item
// is done".
const GOAL_ID = /\bg\d+(?:\/i\d+)?\b/g
const GOAL_ITEM = /\b(item\s+)i\d+\b/gi
// An inline code span: a run of backticks, the text, the same run again. A
// shorter run inside a longer one stays part of the code.
const CODE_SPAN = /(`+)([\s\S]*?)\1/g

// prose drops the markup of text outside inline code: link targets, emphasis,
// and goal IDs. A code span keeps every character it has.
function prose(part: string): string {
  return part
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1') // a link keeps its words
    .replace(/\*{1,3}|~~/g, '') // emphasis marks
    .replace(/`+/g, '') // an unmatched backtick
    .replace(GOAL_ID, '')
    .replace(GOAL_ITEM, '$1')
}

// plainText drops the markup a post body may hold: fence lines, headings,
// quotes, list bullets, emphasis, link targets and goal IDs. Code keeps its
// text, so "use `src/**/*.ts`" never reads "use src//.ts".
function plainText(text: string): string {
  const lines = text
    .replace(/^[ \t]*`{3,}.*$/gm, '') // fence lines, not their text
    .replace(/^[ \t]*(?:#{1,6}\s+|>+\s*)/gm, '') // headings and quotes
    .replace(/^[ \t]*(?:[-*+]|\d{1,2}[.)])\s+/gm, '') // list bullets and numbers
  const parts: string[] = []
  let last = 0
  for (const match of lines.matchAll(CODE_SPAN)) {
    const start = match.index ?? 0
    parts.push(prose(lines.slice(last, start)))
    parts.push(match[2] ?? '') // the code itself, without its backticks
    last = start + match[0].length
  }
  parts.push(prose(lines.slice(last)))
  return parts.join('')
    .replace(/\s+([,.;:])/g, '$1') // punctuation left apart by a goal ID
    .replace(/(?:\s*·\s*){2,}/g, ' · ') // separators left around a goal ID
    .replace(/\s+/g, ' ')
    .trim()
}

// snippet cuts the post text to about width characters, starting at a word
// just before the first matched word, on one line, and marks every match of
// the query words.
export function snippet(text: string, query: string, width = 140): Part[] {
  const flat = plainText(text)
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
  // Cut at a word, not inside one: "…R #288" reads as "…open PR #288". The
  // move never passes the first match, which must stay on screen.
  const space = start > 0 ? flat.indexOf(' ', start) : -1
  if (space > 0 && space < first && space < end) start = space + 1
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
