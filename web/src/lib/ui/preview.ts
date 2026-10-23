// A long post collapses to a short preview, and the preview is real text.
// It ends at the end of a paragraph, at a whole sentence or, when even the
// first sentence is too long, at a word plus an ellipsis. Markdown stays
// valid: the preview holds whole blocks or a fenced code block that it closes.
// The feed, the DM transcript and the forward draft all use postPreview.

/** Characters a collapsed preview may use, about three lines on a phone. */
export const PREVIEW_BUDGET = 140

/** A hidden part shorter than this reads better in full, with no button. */
export const PREVIEW_SMALL = 120

export interface Preview {
  /** Markdown for the collapsed post, empty when no text fits. */
  text: string
  /** True when the post holds more text than the preview shows. */
  truncated: boolean
}

type BlockKind = 'code' | 'table' | 'list' | 'paragraph'

const FENCE = /^\s*(```|~~~)/
const ITEM = /^\s*(?:[-*+]|\d{1,9}[.)])\s+/
const TABLE_RULE = /^\s*\|?\s*:?-+:?\s*(?:\|\s*:?-+:?\s*)*\|?\s*$/
const ABBREVIATION = /(?:^|\s)(?:e\.g|i\.e|etc|vs|Mr|Mrs|Ms|Dr|No|Fig|approx)\.$/i
const NUMBER = /\d\.$/
const INITIAL = /(?:^|\s)[A-Z]\.$/

// blocksOf splits a post at its blank lines. A blank line inside a fenced
// code block does not split it.
function blocksOf(src: string): string[] {
  const out: string[] = []
  let current: string[] = []
  let fence: string | null = null
  const flush = () => {
    const block = current.join('\n').trim()
    if (block) out.push(block)
    current = []
  }
  for (const line of src.split('\n')) {
    const marker = FENCE.exec(line)?.[1]
    if (fence) {
      current.push(line)
      if (marker === fence) fence = null
      continue
    }
    if (marker) {
      flush()
      fence = marker
      current.push(line)
      continue
    }
    if (!line.trim()) {
      flush()
      continue
    }
    current.push(line)
  }
  flush()
  return out
}

function kindOf(block: string): BlockKind {
  const lines = block.split('\n')
  if (FENCE.test(block)) return 'code'
  const header = lines[0] ?? ''
  const rule = lines[1] ?? ''
  if (header.includes('|') && TABLE_RULE.test(rule) && rule.includes('-')) return 'table'
  if (ITEM.test(header)) return 'list'
  return 'paragraph'
}

// trimInline drops markdown the cut left open: a link without its target, an
// odd inline code tick, or a trailing emphasis marker.
function trimInline(s: string): string {
  let out = s.replace(/\s+$/, '')
  for (let pass = 0; pass < 2; pass++) {
    const bracket = out.lastIndexOf('[')
    if (bracket >= 0 && out.indexOf(']', bracket) < 0) out = out.slice(0, bracket).replace(/\s+$/, '')
    const ticks = out.split('`').length - 1
    if (ticks % 2 === 1) {
      const tick = out.lastIndexOf('`')
      out = out.slice(0, tick).replace(/\s+$/, '')
    }
    out = out.replace(/[*_~]+$/, '').replace(/\s+$/, '')
  }
  return out
}

// lastSentenceEnd finds the end of the last complete sentence within limit.
// A full stop inside a number, an abbreviation or an initial does not count.
function lastSentenceEnd(text: string, limit: number): number {
  const ends = /[.!?…]["'”’)\]]*(?=\s)/g
  let end = 0
  for (let m = ends.exec(text); m; m = ends.exec(text)) {
    const stop = m.index + m[0].length
    if (stop > limit) break
    const head = text.slice(0, m.index + 1)
    if (NUMBER.test(head) || ABBREVIATION.test(head) || INITIAL.test(head)) continue
    end = stop
  }
  return end
}

// cutParagraph cuts one block of prose. It prefers the end of a sentence and
// falls back to a word, which it marks with an ellipsis.
function cutParagraph(text: string, budget: number): Preview {
  const src = text.trim()
  if (src.length <= budget) return { text: src, truncated: false }
  const limit = Math.max(1, budget)
  const end = lastSentenceEnd(src, limit)
  if (end > 0) {
    const whole = trimInline(src.slice(0, end))
    if (whole) return { text: whole, truncated: true }
  }
  const head = src.slice(0, limit)
  const space = head.search(/\s+\S*$/)
  const word = trimInline(space > 0 ? head.slice(0, space) : head)
  if (!word) return { text: '', truncated: true }
  return { text: word + '…', truncated: true }
}

// listItems groups a list into its items, keeping continuation lines with the
// item they belong to.
function listItems(block: string): string[] {
  const items: string[] = []
  for (const line of block.split('\n')) {
    if (ITEM.test(line) || items.length === 0) items.push(line)
    else items[items.length - 1] += '\n' + line
  }
  return items
}

// listPreview keeps whole items while they fit. A first item that is too long
// keeps its marker and gets its text cut.
function listPreview(block: string, budget: number): string {
  const kept: string[] = []
  let used = 0
  for (const item of listItems(block)) {
    const add = kept.length ? item.length + 1 : item.length
    if (used + add > budget) break
    kept.push(item)
    used += add
  }
  if (kept.length) return kept.join('\n')
  const first = listItems(block)[0] ?? ''
  const marker = ITEM.exec(first)?.[0] ?? ''
  const cut = cutParagraph(first.slice(marker.length), Math.max(1, budget - marker.length))
  return cut.text ? marker + cut.text : ''
}

// codePreview closes the fence it opens, so the preview never leaves a code
// block open.
function codePreview(block: string, budget: number): string {
  const lines = block.split('\n')
  const open = FENCE.exec(lines[0] ?? '')?.[1] ?? '```'
  const body = lines.slice(1).filter((line) => !/^\s*(```|~~~)\s*$/.test(line))
  const room = Math.max(1, budget - open.length * 2 - 2)
  const kept: string[] = []
  let used = 0
  for (const line of body) {
    const add = kept.length ? line.length + 1 : line.length
    if (used + add > room) break
    kept.push(line)
    used += add
  }
  if (!kept.length) {
    const first = (body[0] ?? '').slice(0, room).trimEnd()
    if (first) kept.push(first + '…')
  }
  if (!kept.length) return ''
  return [lines[0], ...kept, open].join('\n')
}

// tablePreview keeps the header and the rule that makes a table valid, plus
// whole rows while they fit.
function tablePreview(block: string, budget: number): string {
  const lines = block.split('\n')
  const kept = lines.slice(0, 2)
  let used = kept.join('\n').length
  for (const line of lines.slice(2)) {
    if (used + line.length + 1 > budget) break
    kept.push(line)
    used += line.length + 1
  }
  return kept.join('\n')
}

export function postPreview(text: string, budget = PREVIEW_BUDGET): Preview {
  const src = (text || '').replace(/\r\n?/g, '\n').trim()
  if (!src) return { text: '', truncated: false }
  if (src.length <= budget) return { text: src, truncated: false }
  const blocks = blocksOf(src)
  if (!blocks.length) return { text: '', truncated: false }
  const kept: string[] = []
  let used = 0
  for (const block of blocks) {
    const add = kept.length ? block.length + 2 : block.length
    if (used + add > budget) break
    kept.push(block)
    used += add
  }
  if (kept.length) return smallTail(src, kept.join('\n\n'))
  // The first block alone is too long. Keep it whole where markdown allows,
  // and cut its text at a natural boundary otherwise.
  const first = blocks[0] ?? ''
  switch (kindOf(first)) {
    case 'code': return smallTail(src, codePreview(first, budget))
    case 'table': return smallTail(src, tablePreview(first, budget))
    case 'list': return smallTail(src, listPreview(first, budget))
    default: {
      const cut = cutParagraph(first, budget)
      return smallTail(src, cut.text)
    }
  }
}

// smallTail shows the whole post when the hidden part is too short to save
// the reader anything. An empty preview is a real cut, not a short tail.
function smallTail(src: string, preview: string): Preview {
  if (!preview) return { text: '', truncated: true }
  if (src.length - preview.length <= PREVIEW_SMALL) return { text: src, truncated: false }
  return { text: preview, truncated: true }
}
