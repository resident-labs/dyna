// toolCallLabel builds the title and the detail of one activity-timeline tool row.
// The harness summary already names the kind ("read 2 files"), and the row used
// to add that kind again, plus the tool id. The title says the work once.
// Several files use a count, such as "Read 2 files", and paths lists each file.

export interface ToolCallFields {
  step?: string
  label?: string
  toolName?: string
  tool?: string
  target?: string
}

export interface ToolCallText {
  title: string
  detail: string
  paths: string[]
}

const stepWords: Record<string, string> = {
  read: 'Read', edit: 'Edit', write: 'Write', search: 'Search', list: 'List', run: 'Command',
  fetch: 'Fetch', delegate: 'Delegate', plan: 'Plan', monitor: 'Monitor', tool: 'Tool',
}

const verbWords: Record<string, string> = {
  read: 'Read', edited: 'Edit', wrote: 'Write', listed: 'List', 'searched for': 'Search',
}

// Headline in internal/transcript writes "read 2 files (a, b)" for a grouped call.
const groupedCall = /^(read|edited|wrote|listed|searched for)\s+(\d+)\s+(files|patterns)(?:\s+\((.*)\))?\s*$/i

// A summary that is only the verb adds nothing next to the kind icon.
const bareVerb = /^(read|edited|wrote|listed|searched for|ran|fetched|called|searched)$/i

function stepWord(kind: string | undefined): string {
  return stepWords[kind ?? ''] ?? 'Tool'
}

function same(left: string, right: string): boolean {
  return left.trim().toLowerCase() === right.trim().toLowerCase()
}

// coversKind is true when text already starts with the kind word or its verb.
// "edited" starts with "Edit". "wrote" does not start with "Write", so that
// form is named here. "ran" does not start with "Command".
function coversKind(text: string, step: string | undefined, word: string): boolean {
  const value = text.trim().toLowerCase()
  const kind = word.trim().toLowerCase()
  if (!value || !kind) return false
  if (value === kind || value.startsWith(`${kind} `)) return true
  const first = value.split(/\s+/, 1)[0] ?? ''
  if (first.startsWith(kind) && first !== kind) return true
  return step === 'write' && (first === 'wrote' || first.startsWith('wrote'))
}

function grouped(tool: string): { word: string; count: number; noun: string; paths: string[] } | undefined {
  const match = groupedCall.exec(tool.trim())
  if (!match) return undefined
  const verb = (match[1] ?? '').toLowerCase()
  const count = Number(match[2])
  const noun = (match[3] ?? '').toLowerCase()
  if (!Number.isInteger(count) || count < 2) return undefined
  if (noun !== 'files' && noun !== 'patterns') return undefined
  const paths = (match[4] ?? '').split(', ').map((path) => path.trim()).filter((path) => path !== '' && path !== '…')
  return { word: verbWords[verb] ?? 'Tool', count, noun, paths }
}

export function toolCallLabel(fields: ToolCallFields): ToolCallText {
  const step = fields.step
  const word = stepWord(step)
  const label = fields.label?.trim() ?? ''
  const target = fields.target?.trim() ?? ''
  const summary = fields.tool?.trim() ?? ''
  const toolName = fields.toolName?.trim() ?? ''
  const group = grouped(summary)
  const titleWord = word === 'Tool' && group ? group.word : word
  const title = group ? `${titleWord} ${group.count} ${group.noun}` : (label || target || titleWord)
  const paths = group && group.paths.length > 0 ? group.paths : (target && target !== title ? [target] : [])

  const parts: string[] = []
  if (!coversKind(summary, step, titleWord) && !coversKind(title, step, titleWord)) parts.push(titleWord)
  if (toolName && !same(toolName, titleWord) && !same(toolName, title) && !coversKind(toolName, step, titleWord)) {
    parts.push(toolName)
  }
  if (summary && !group && !bareVerb.test(summary) && !same(summary, title) && !same(summary, label)) {
    parts.push(summary)
  }
  return { title, detail: parts.join(' · '), paths }
}
