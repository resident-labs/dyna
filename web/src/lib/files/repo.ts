// Helpers for the Files and Changes views: unified diff parsing, git status
// labels, and path pieces. No transport here; api.ts makes the calls.

export type DiffKind = 'modified' | 'added' | 'deleted' | 'renamed' | 'binary'

// a and b are the old and new line numbers (0 when the side has none).
export interface DiffLine {
  t: '+' | '-' | ' ' | '\\'
  text: string
  a: number
  b: number
}

export interface DiffHunk {
  header: string
  context: string
  lines: DiffLine[]
}

export interface DiffFile {
  path: string
  from: string
  kind: DiffKind
  hunks: DiffHunk[]
  adds: number
  dels: number
}

// parseDiff splits `git diff` text into files.
export function parseDiff(text?: string | null): DiffFile[] {
  const files: DiffFile[] = []
  let f: DiffFile | null = null
  let h: DiffHunk | null = null
  let a = 0
  let b = 0
  for (const line of (text || '').split('\n')) {
    if (line.startsWith('diff --git ')) {
      const m = line.match(/^diff --git a\/(.*) b\/(.*)$/)
      f = { path: m?.[2] ?? line.slice(11), from: m?.[1] ?? '', kind: 'modified', hunks: [], adds: 0, dels: 0 }
      files.push(f)
      h = null
      continue
    }
    if (!f) continue
    if (!h) {
      if (line.startsWith('new file')) f.kind = 'added'
      else if (line.startsWith('deleted file')) f.kind = 'deleted'
      else if (line.startsWith('rename from ')) { f.kind = 'renamed'; f.from = line.slice(12) }
      else if (line.startsWith('rename to ')) f.path = line.slice(10)
      else if (line.startsWith('Binary files') || line.startsWith('GIT binary patch')) f.kind = 'binary'
      else if (line.startsWith('+++ ') && line !== '+++ /dev/null') f.path = line.slice(4).replace(/^b\//, '')
    }
    const hm = line.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/)
    if (hm) {
      a = Number(hm[1])
      b = Number(hm[2])
      h = { header: line, context: (hm[3] ?? '').trim(), lines: [] }
      f.hunks.push(h)
      continue
    }
    if (!h) continue
    const t = line[0]
    if (t === '+') { h.lines.push({ t, text: line.slice(1), a: 0, b: b++ }); f.adds++ }
    else if (t === '-') { h.lines.push({ t, text: line.slice(1), a: a++, b: 0 }); f.dels++ }
    else if (t === ' ') h.lines.push({ t, text: line.slice(1), a: a++, b: b++ })
    else if (t === '\\') h.lines.push({ t, text: line.slice(2), a: 0, b: 0 })
  }
  return files
}

// diffTotals counts the files, additions and deletions of parsed diff files.
export function diffTotals(files: Pick<DiffFile, 'adds' | 'dels'>[]) {
  return { files: files.length, adds: files.reduce((n, f) => n + f.adds, 0), dels: files.reduce((n, f) => n + f.dels, 0) }
}

export type StatusCode = 'M' | 'A' | 'D' | 'R' | 'C' | 'T' | 'U' | '?'
export type Tone = 'gold' | 'sage' | 'coral' | 'iris'

// One letter and a word for each git status, in the app's color language:
// gold is changed, sage is new, coral is gone or in conflict.
export const STATUS: Record<StatusCode, { word: string; tone: Tone }> = {
  M: { word: 'Modified', tone: 'gold' },
  A: { word: 'Added', tone: 'sage' },
  D: { word: 'Deleted', tone: 'coral' },
  R: { word: 'Renamed', tone: 'iris' },
  C: { word: 'Copied', tone: 'iris' },
  T: { word: 'Type changed', tone: 'gold' },
  U: { word: 'Conflict', tone: 'coral' },
  '?': { word: 'Untracked', tone: 'sage' },
}

const isStatusCode = (c: string): c is StatusCode => Object.hasOwn(STATUS, c)

// A status row from the Changes view. Only the fields the letter reads.
export interface StatusEntry {
  status?: string
  indexStatus?: string
  worktreeStatus?: string
  conflicted?: boolean
  untracked?: boolean
}

export type StatusSide = 'staged' | 'unstaged' | 'untracked'

// statusLetter picks the letter to show for a status row and one side.
export function statusLetter(f: StatusEntry, side?: StatusSide): StatusCode {
  if (f.conflicted) return 'U'
  if (f.untracked) return '?'
  const c = side === 'staged' ? f.indexStatus : side === 'unstaged' ? f.worktreeStatus : f.status
  const l = (c && c !== '.' ? c : f.status || 'M').charAt(0).toUpperCase()
  return isStatusCode(l) ? l : 'M'
}

// split returns [folder with a trailing slash, name] for a repo path.
export function split(path: string): [string, string] {
  const i = path.lastIndexOf('/')
  return i < 0 ? ['', path] : [path.slice(0, i + 1), path.slice(i + 1)]
}

export interface Crumb {
  name: string
  path: string
}

// crumbs turns "a/b/c" into [{name: 'a', path: 'a'}, {name: 'b', path: 'a/b'}, ...].
export function crumbs(path?: string | null): Crumb[] {
  const out: Crumb[] = []
  let acc = ''
  for (const name of (path || '').split('/').filter(Boolean)) {
    acc = acc ? `${acc}/${name}` : name
    out.push({ name, path: acc })
  }
  return out
}

export function parent(path?: string | null): string {
  const p = path || ''
  const i = p.lastIndexOf('/')
  return i < 0 ? '' : p.slice(0, i)
}

// base is the last part of a host path, for a short root label.
export function base(path?: string | null): string {
  return (path || '').replace(/\/+$/, '').split('/').pop() || path || ''
}

const MARKDOWN = /\.(md|markdown|mdx)$/i
export const isMarkdown = (name: string, language = ''): boolean => MARKDOWN.test(name) || /^markdown$/i.test(language)

// A row of a folder listing from the workspace. Only the fields the tree reads.
export interface RepoEntry {
  path: string
  name: string
  kind: 'dir' | 'file' | 'symlink'
  dir?: boolean
}

export const isDir = (e: RepoEntry): boolean => e.kind === 'dir' || (e.kind === 'symlink' && e.dir === true)

// The state of one folder: its rows once loaded, or why it is loading or failed.
export interface Listing {
  entries?: RepoEntry[]
  loading?: boolean
  error?: string
}

export interface TreeNote {
  kind: 'note'
  text: string
  depth: number
  path: string
  error?: boolean
}

export interface TreeEntry extends RepoEntry {
  entry: RepoEntry
  depth: number
  isDir: boolean
  expanded: boolean
}

export type TreeRow = TreeNote | TreeEntry

// treeRows lists the visible rows of the folder tree, depth first through the
// open folders. A row that did not change keeps its object from `previous`
// (a path -> row map), so the view does not redo it when another folder opens.
export function treeRows(
  lists: Readonly<Record<string, Listing | undefined>>,
  open: ReadonlySet<string>,
  previous: ReadonlyMap<string, TreeRow> = new Map(),
): TreeRow[] {
  const out: TreeRow[] = []
  const walk = (path: string, depth: number): void => {
    const l = lists[path]
    const entries = l?.entries
    if (!entries) {
      if (l?.loading) out.push({ kind: 'note', text: 'Loading…', depth, path: path + '/…' })
      if (l?.error) out.push({ kind: 'note', text: l.error, depth, path: path + '/!', error: true })
      return
    }
    if (!entries.length && path) out.push({ kind: 'note', text: 'Empty folder', depth, path: path + '/∅' })
    for (const e of entries) {
      const d = isDir(e)
      const expanded = d && open.has(e.path)
      const old = previous.get(e.path)
      if (old && 'entry' in old && old.entry === e && old.depth === depth && old.expanded === expanded) out.push(old)
      else out.push({ ...e, entry: e, depth, isDir: d, expanded })
      if (expanded) walk(e.path, depth + 1)
    }
  }
  walk('', 0)
  return out
}
