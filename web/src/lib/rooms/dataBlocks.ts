// Splits a workflow post into prose and JSON data. A script sends data as a
// fenced json block (post(to, text, data)), and an older script may inline a
// JSON object on one line, so both read as data. Only an object or a non-empty
// array counts: a bare number or string in a code fence stays text.
export type JsonValue = string | number | boolean | null | JsonValue[] | { [key: string]: JsonValue }

export type PostSegment = { kind: 'text'; text: string } | { kind: 'data'; value: JsonValue[] | { [key: string]: JsonValue } }

type Data = Extract<PostSegment, { kind: 'data' }>['value']

const FENCE = /^\s*```(\w*)\s*$/
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
    const fence = FENCE.exec(line)
    if (fence) {
      const end = lines.findIndex((l, j) => j > i && FENCE.test(l))
      if (end > i) {
        const value = fence[1]?.toLowerCase() === 'json' ? parseData(lines.slice(i + 1, end).join('\n')) : undefined
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
