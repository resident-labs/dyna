import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import type { Attachment } from '../api/room.ts'

const parser = new Marked({ gfm: true, breaks: false })
export const TEXT_LIMIT = 2 * 1024 * 1024
export const TABLE_ROWS = 500
export const TABLE_COLUMNS = 100

export type PreviewKind = 'card' | 'image' | 'markdown' | 'table' | 'json' | 'word' | 'pdf' | 'text' | 'file'

// The fields of an attachment that decide its preview kind.
export type PreviewSource = Partial<Pick<Attachment, 'name' | 'mime' | 'render'>>

export function previewKind(att: PreviewSource): PreviewKind {
  if (att.render === 'html') return 'card'
  const mime = ((att.mime || '').split(';')[0] ?? '').toLowerCase()
  const ext = ((att.name || '').split('.').pop() ?? '').toLowerCase()
  if (/^image\/(png|jpeg|gif|webp)$/.test(mime)) return 'image'
  if (['md', 'markdown', 'mdown'].includes(ext) || mime === 'text/markdown') return 'markdown'
  if (['csv', 'tsv'].includes(ext) || ['text/csv', 'text/tab-separated-values'].includes(mime)) return 'table'
  if (ext === 'json' || /^(application|text)\/([\w.+-]*\+)?json$/.test(mime)) return 'json'
  if (ext === 'docx' || mime === 'application/vnd.openxmlformats-officedocument.wordprocessingml.document') return 'word'
  if (ext === 'pdf' || mime === 'application/pdf') return 'pdf'
  if (mime.startsWith('text/') || ['application/xml', 'application/javascript', 'application/yaml'].includes(mime)) return 'text'
  return 'file'
}

const LABELS: Record<PreviewKind, string> = {
  card: 'HTML card', image: 'Image', markdown: 'Markdown', table: 'Table',
  json: 'JSON', word: 'Word document', pdf: 'PDF document', text: 'Text file', file: 'File',
}

export const previewLabel = (kind: PreviewKind): string => LABELS[kind] || 'File'

// Only document markup reaches the app DOM. Links open through the viewer.
export function documentHTML(html: string): string {
  const fragment = DOMPurify.sanitize(html, {
    RETURN_DOM_FRAGMENT: true,
    USE_PROFILES: { html: true },
    FORBID_TAGS: ['style', 'form', 'button', 'textarea', 'select', 'video', 'audio'],
    FORBID_ATTR: ['style', 'srcset'],
  })
  for (const input of fragment.querySelectorAll('input')) {
    if (input.type === 'checkbox') input.disabled = true
    else input.remove()
  }
  for (const link of fragment.querySelectorAll('a')) {
    const href = link.getAttribute('href') || ''
    if (!/^(https?:\/\/|mailto:|#)/i.test(href)) link.removeAttribute('href')
    link.removeAttribute('target')
    link.setAttribute('rel', 'noopener noreferrer')
  }
  const div = document.createElement('div')
  div.append(fragment)
  return div.innerHTML
}

export function markdownDocument(text: string): string {
  return documentHTML(parser.parse(text, { async: false }))
}

export interface TableResult {
  rows: string[][]
  more: boolean
  columnsTruncated: boolean
}

// A bounded RFC 4180 reader. Quoted separators, doubled quotes, CRLF, and
// quoted line breaks remain cell content. Extra rows stay in the download.
export function parseTable(text: string, delimiter = ','): TableResult {
  text = text.replace(/^\uFEFF/, '')
  const rows: string[][] = []
  let row: string[] = [], cell = '', quoted = false, closed = false, columnsTruncated = false
  const field = () => {
    if (row.length < TABLE_COLUMNS) row.push(cell)
    else columnsTruncated = true
    cell = ''; closed = false
  }
  const record = () => { field(); rows.push(row); row = [] }
  for (let i = 0; i < text.length; i++) {
    const ch = text.charAt(i)
    if (quoted) {
      if (ch === '"' && text.charAt(i + 1) === '"') { cell += '"'; i++ }
      else if (ch === '"') { quoted = false; closed = true }
      else cell += ch
    } else if (ch === '"' && !cell && !closed) quoted = true
    else if (ch === delimiter) field()
    else if (ch === '\n' || ch === '\r') {
      if (ch === '\r' && text.charAt(i + 1) === '\n') i++
      record()
      if (rows.length > TABLE_ROWS + 1) return { rows: rows.slice(0, TABLE_ROWS + 1), more: true, columnsTruncated }
    } else {
      if (closed || ch === '"') throw new Error('The table has an unexpected quote. Open Source to read the original file.')
      cell += ch
    }
  }
  if (quoted) throw new Error('The table has an unfinished quoted cell. Open Source to read the original file.')
  if (cell || row.length || closed) record()
  return { rows: rows.slice(0, TABLE_ROWS + 1), more: rows.length > TABLE_ROWS + 1, columnsTruncated }
}

// Validate JSON, then indent its original tokens. Parsing and serializing
// would round large integers and could change the values shown in a file.
export function formatJSON(text: string): string {
  JSON.parse(text)
  const tokens: string[] = text.match(/"(?:[^"\\]|\\.)*"|[{}\[\],:]|[^\s{}\[\],:]+/g) || []
  let out = '', depth = 0
  const line = () => { out += '\n' + '  '.repeat(Math.min(depth, 40)) }
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i] ?? ''
    if (token === '{' || token === '[') {
      out += token
      depth++
      if (tokens[i + 1] !== (token === '{' ? '}' : ']')) line()
    } else if (token === '}' || token === ']') {
      depth--
      if (tokens[i - 1] !== (token === '}' ? '{' : '[')) line()
      out += token
    } else if (token === ',') { out += ','; line() }
    else if (token === ':') out += ': '
    else out += token
  }
  return out
}
