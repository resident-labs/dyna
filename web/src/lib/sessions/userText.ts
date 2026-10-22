// A prompt reaches an agent with blocks Dyna appends: the attachment list and
// the instructions to open the files. Those lines are for the agent. Every
// view that shows such text hides them and shows the files as cards.
import type { Attachment } from '../api/room.ts'

// Dyna writes the list as "Attachments (n), saved with the room:" or, when
// space is short, "Attachments (n): dyna room read <room> <post>".
// An attachment as the list names it: the fields a card needs, no checksum.
export type ManifestItem = Pick<Attachment, 'id' | 'name' | 'mime' | 'size'> & Partial<Pick<Attachment, 'render' | 'title' | 'visual' | 'version'>>

const BLOCK = /\n*^Attachments \(\d+\)(?:, saved with the room:|: dyna room read )[\s\S]*$/m
const ITEM = /^- (.+?) · /
const FULL = /^- (.+?) · (.+?) · (\d+(?:\.\d)?) (B|KB|MB) · (.+)$/
const STORED = /([0-9a-f]{24})(?:\.[A-Za-z0-9]+)?$/
const UNIT: Record<string, number> = { B: 1, KB: 1024, MB: 1024 * 1024 }

// manifestItem turns one list line into the attachment fields a card needs.
// The stored path ends in the attachment ID. A line with no ID is dropped
// because it cannot open.
function manifestItem(line: string): ManifestItem | null {
  const m = line.match(FULL)
  const id = m?.[5]?.match(STORED)?.[1]
  if (!m || !id) return null
  const [, name = '', kind = '', amount = '0', unit = 'B'] = m
  const size = Math.round(Number(amount) * (UNIT[unit] ?? 1))
  // Go writes `HTML card "Title", visual <post-id> v<n>`.
  const card = kind.match(/^HTML card(?: "(.+?)")?(?:, visual (msg_[0-9a-f]+) v(\d+))?$/)
  if (!card) return { id, name, mime: kind, size }
  const [, title, visual, version] = card
  // The first version's visual is its own post, so it is not an update.
  const update = Number(version) > 1 ? { visual } : {}
  return { id, name, mime: 'text/html', render: 'html', title: title || name, ...update, ...(version ? { version: Number(version) } : {}), size }
}

// userText returns {text, files, items}. text is what the sender typed.
// files are the names from the list. items are attachment objects for the
// lines that name a stored file, for text that has no attachment data of its
// own.
export interface UserText {
  text: string
  files: string[]
  items: ManifestItem[]
}
export function userText(raw?: string | null): UserText {
  const s = String(raw || '')
  const at = s.search(BLOCK)
  if (at < 0) return { text: s.trim(), files: [], items: [] }
  const lines = s.slice(at).split('\n')
  const files = lines.flatMap((l) => l.match(ITEM)?.[1] ?? [])
  const items = lines.flatMap((l) => manifestItem(l) ?? [])
  return { text: s.slice(0, at).trim(), files, items }
}
