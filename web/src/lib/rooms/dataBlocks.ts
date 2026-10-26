// Splits a workflow post into prose and JSON data. A script sends data as a
// fenced json block (post(to, text, data)), and an older script may inline a
// JSON object on one line, so both read as data. Only an object or a non-empty
// array counts: a bare number or string in a code fence stays text.
export type JsonValue = string | number | boolean | null | JsonValue[] | { [key: string]: JsonValue }

export type PostSegment = { kind: 'text'; text: string } | { kind: 'data'; value: JsonValue[] | { [key: string]: JsonValue } }

type Data = Extract<PostSegment, { kind: 'data' }>['value']

const FENCE_OPEN = /^ {0,3}(`{3,}|~{3,})(.*)$/

// fenceEnd finds the line that closes the Markdown fence opened at line i: the
// same character, at least as long, with nothing after it. An unclosed fence
// runs to the end of the text, as Markdown reads it. It returns -1 when line i
// opens no fence; the info string is the opener's text after the marker.
function fenceEnd(lines: string[], i: number): { end: number; info: string; closed: boolean } {
  const open = FENCE_OPEN.exec(lines[i] ?? '')
  const marker = open?.[1]
  const info = (open?.[2] ?? '').trim()
  if (!marker || (marker.startsWith('`') && info.includes('`'))) return { end: -1, info: '', closed: false }
  const closer = new RegExp(`^ {0,3}${marker[0] === '`' ? '`' : '~'}{${marker.length},}[ \\t]*$`)
  const end = lines.findIndex((l, j) => j > i && closer.test(l))
  return end < 0 ? { end: lines.length - 1, info, closed: false } : { end, info, closed: true }
}
// A label, then JSON on the same line: "LANDING REQUEST after review: {…}".
const LABELLED = /^(.*?:\s+)([{[].*)$/

function parseData(raw: string): Data | undefined {
  const s = raw.trim()
  if (!(s.startsWith('{') && s.endsWith('}')) && !(s.startsWith('[') && s.endsWith(']'))) return undefined
  try {
    // JSON.parse yields only JSON values, which is what JsonValue describes.
    const v: unknown = JSON.parse(s)
    if (Array.isArray(v)) return v.length ? (v as JsonValue[]) : undefined
    if (v !== null && typeof v === 'object') return Object.keys(v).length ? (v as { [key: string]: JsonValue }) : undefined
  } catch {
    return undefined
  }
  return undefined
}

export function splitPost(text: string): PostSegment[] {
  const out: PostSegment[] = []
  let prose: string[] = []
  const flush = () => {
    const joined = prose.join('\n').trim()
    if (joined) out.push({ kind: 'text', text: joined })
    prose = []
  }
  const lines = text.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? ''
    const { end, info, closed } = fenceEnd(lines, i)
    if (end >= i) {
      const value = closed && info.toLowerCase() === 'json' ? parseData(lines.slice(i + 1, end).join('\n')) : undefined
      if (value) {
        flush()
        out.push({ kind: 'data', value })
      } else {
        // Any other code block stays whole: JSON inside it is code, and
        // splitting it would leave its closing fence to open a new block.
        prose.push(...lines.slice(i, end + 1))
      }
      i = end
      continue
    }
    const whole = parseData(line)
    if (whole) {
      flush()
      out.push({ kind: 'data', value: whole })
      continue
    }
    const labelled = LABELLED.exec(line)
    const tail = labelled?.[2] ? parseData(labelled[2]) : undefined
    if (labelled && tail) {
      prose.push((labelled[1] ?? '').trimEnd())
      flush()
      out.push({ kind: 'data', value: tail })
      continue
    }
    prose.push(line)
  }
  flush()
  return out
}

// A path or a hash reads as code: the owner copies it, never reads it as prose.
export function codeLike(s: string): boolean {
  return /^(?:~|\.{0,2})\/\S+$/.test(s) || /^[0-9a-f]{7,64}$/i.test(s) || /^[\w.-]+\/[\w./-]+$/.test(s) || /^(?:wf|msg|ses|room)_[\w-]+$/.test(s)
}
