// Text rules for the command palette and its New room path mode. Pure
// functions, so the component stays small.

// isPathText is true while the text names a folder: it starts with /, ~/ or
// ~ alone.
export function isPathText(text: string) {
  return text.startsWith('/') || text === '~' || text.startsWith('~/')
}

// splitPath cuts the text at its last slash. dir keeps the slash; filter is
// what follows it.
export function splitPath(text: string) {
  const i = text.lastIndexOf('/')
  if (i < 0) return { dir: '', filter: text }
  return { dir: text.slice(0, i + 1), filter: text.slice(i + 1) }
}

// folderRows lists the folders that match the filter. Hidden folders (a
// leading dot) show only when the filter itself starts with a dot.
export function folderRows<T extends { name: string }>(entries: T[] | null | undefined, filter: string) {
  const f = filter.toLowerCase()
  const dots = f.startsWith('.')
  return (entries ?? []).filter((e) => (dots || !e.name.startsWith('.')) && e.name.toLowerCase().startsWith(f))
}

// parentOf is the folder above a directory text such as "/a/b/". ~ expands
// to home first. The root has no parent.
export function parentOf(dir: string, home: string) {
  if (dir === '~' || dir.startsWith('~/')) dir = home.replace(/\/$/, '') + dir.slice(1)
  const trimmed = dir.replace(/\/+$/, '')
  const i = trimmed.lastIndexOf('/')
  return i <= 0 ? '/' : trimmed.slice(0, i + 1)
}

// atRoot is true when the directory text is the top of the file system.
export function atRoot(dir: string) {
  return /^\/+$/.test(dir)
}

// showHome writes a path under home as ~/…, for display.
export function showHome(path: string, home: string) {
  const h = home.replace(/\/$/, '')
  if (path === h || path === h + '/') return '~/'
  return path.startsWith(h + '/') ? '~' + path.slice(h.length) : path
}

// matches is true when every word of the query is a substring of one of the
// fields, ignoring case.
export function matches(query: string, ...fields: (string | null | undefined)[]) {
  const hay = fields.filter(Boolean).join(' ').toLowerCase()
  return query
    .toLowerCase()
    .split(/\s+/)
    .filter(Boolean)
    .every((w) => hay.includes(w))
}
