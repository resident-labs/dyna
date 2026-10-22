// LibraryView is librarian.LibraryView in internal/librarian/view.go. Its
// entries are library.Entry and its lint findings are library.Finding.
export interface LibraryEntry {
  id: string
  name: string
  kind: string
  parent?: string
  summary: string
  differs?: string
  owns: string[]
  docs?: string[]
  tests?: string[]
  relates?: string[]
  verifiedAt?: string
  retired?: boolean
  successor?: string
}

export interface LibraryFinding {
  rule: number
  entry?: string
  file?: string
  message: string
}

export interface LibraryView {
  repo: string
  enabled: boolean
  head?: string
  lastRun?: string
  entries: LibraryEntry[]
  stale: string[]
  lint: LibraryFinding[]
}
