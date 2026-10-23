// Syntax highlighting for the file viewer, loaded on first use so the main
// bundle stays small. highlight.js core with the languages agents touch
// most; anything else shows as plain text.
import type { HLJSApi, LanguageFn } from 'highlight.js'

type LanguageLoader = [name: string, load: () => Promise<{ default: LanguageFn }>]

const LANGUAGES: LanguageLoader[] = [
  ['go', () => import('highlight.js/lib/languages/go')],
  ['javascript', () => import('highlight.js/lib/languages/javascript')],
  ['typescript', () => import('highlight.js/lib/languages/typescript')],
  ['json', () => import('highlight.js/lib/languages/json')],
  ['css', () => import('highlight.js/lib/languages/css')],
  ['xml', () => import('highlight.js/lib/languages/xml')],
  ['markdown', () => import('highlight.js/lib/languages/markdown')],
  ['bash', () => import('highlight.js/lib/languages/bash')],
  ['python', () => import('highlight.js/lib/languages/python')],
  ['yaml', () => import('highlight.js/lib/languages/yaml')],
  ['rust', () => import('highlight.js/lib/languages/rust')],
  ['ini', () => import('highlight.js/lib/languages/ini')],
  ['sql', () => import('highlight.js/lib/languages/sql')],
  ['diff', () => import('highlight.js/lib/languages/diff')],
  ['dockerfile', () => import('highlight.js/lib/languages/dockerfile')],
  ['makefile', () => import('highlight.js/lib/languages/makefile')],
  ['c', () => import('highlight.js/lib/languages/c')],
  ['cpp', () => import('highlight.js/lib/languages/cpp')],
  ['java', () => import('highlight.js/lib/languages/java')],
  ['ruby', () => import('highlight.js/lib/languages/ruby')],
  ['php', () => import('highlight.js/lib/languages/php')],
  ['swift', () => import('highlight.js/lib/languages/swift')],
  ['kotlin', () => import('highlight.js/lib/languages/kotlin')],
  ['lua', () => import('highlight.js/lib/languages/lua')],
]

async function loadLanguage([name, importLanguage]: LanguageLoader): Promise<[string, LanguageFn]> {
  const mod = await importLanguage()
  return [name, mod.default]
}

let ready: Promise<HLJSApi> | null = null

function load(): Promise<HLJSApi> {
  ready ??= (async () => {
    const [core, languages] = await Promise.all([
      import('highlight.js/lib/core'),
      Promise.all(LANGUAGES.map(loadLanguage)),
    ])
    const hljs = core.default
    for (const [name, language] of languages) hljs.registerLanguage(name, language)
    return hljs
  })()
  return ready
}

// A lookup table. Reading a key that is not in the table gives undefined, but
// a plain object also answers inherited keys such as "constructor" with a
// non-string value. languageOf passes those through, so highlight checks the
// type before it uses the value.
type Table = Readonly<Partial<Record<string, string>>>

// File extensions and names to highlight.js languages. The server's
// language field wins when it names one we have.
const EXT: Table = {
  go: 'go', mod: 'go', js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'javascript',
  ts: 'typescript', tsx: 'typescript', json: 'json', jsonc: 'json', css: 'css', scss: 'css',
  html: 'xml', htm: 'xml', xml: 'xml', svg: 'xml', svelte: 'xml', vue: 'xml',
  md: 'markdown', markdown: 'markdown', sh: 'bash', bash: 'bash', zsh: 'bash', fish: 'bash',
  py: 'python', yml: 'yaml', yaml: 'yaml', rs: 'rust', toml: 'ini', ini: 'ini', conf: 'ini', cfg: 'ini',
  sql: 'sql', diff: 'diff', patch: 'diff', c: 'c', h: 'c', cc: 'cpp', cpp: 'cpp', hpp: 'cpp',
  java: 'java', rb: 'ruby', php: 'php', swift: 'swift', kt: 'kotlin', lua: 'lua',
}
const NAMES: Table = { dockerfile: 'dockerfile', makefile: 'makefile', 'go.sum': '', '.bashrc': 'bash', '.zshrc': 'bash' }
const ALIAS: Table = { 'go.mod': 'go', html: 'xml', svelte: 'xml', shell: 'bash', sh: 'bash', toml: 'ini', js: 'javascript', ts: 'typescript' }
const EXT_LANGUAGES = Object.values(EXT)

// languageOf names the highlight.js language for a file. The result is a
// string for every file name except inherited keys (such as "constructor"),
// which pass through as the object they name; highlight rejects those.
export function languageOf(name: string, hint: string | null | undefined = ''): unknown {
  const h = (hint || '').toLowerCase()
  if (h && (EXT_LANGUAGES.includes(h) || EXT[h] || ALIAS[h])) return ALIAS[h] || EXT[h] || h
  const lower = name.toLowerCase()
  if (lower in NAMES) return NAMES[lower]
  const ext = lower.includes('.') ? (lower.split('.').pop() ?? '') : ''
  return EXT[ext] || ''
}

const ESCAPES = new Map([['&', '&amp;'], ['<', '&lt;'], ['>', '&gt;'], ['"', '&quot;']])
const escape = (s: string): string => s.replace(/[&<>"]/g, (c) => ESCAPES.get(c) ?? c)

// Highlighting a huge file blocks the page; past these limits it stays plain.
const MAX_CHARS = 300_000

// highlight resolves to safe HTML for text. Plain text comes back escaped.
export async function highlight(text: string, lang: unknown): Promise<string> {
  if (typeof lang !== 'string' || !lang || text.length > MAX_CHARS) return escape(text)
  try {
    const hljs = await load()
    return hljs.highlight(text, { language: lang, ignoreIllegals: true }).value
  } catch {
    return escape(text)
  }
}

// splitLines cuts highlighted HTML into one string per line. A span that
// crosses a line end (a block comment, a long string) is closed at the end
// of the line and opened again on the next, so each line stands alone.
export function splitLines(html: string): string[] {
  const out: string[] = []
  const open: string[] = [] // the opening tags in force
  let line = ''
  const re = /(<span[^>]*>)|(<\/span>)|(\n)|([^<\n]+|<)/g
  let m: RegExpExecArray | null
  while ((m = re.exec(html))) {
    const [, tag, close, newline, text] = m
    if (tag) { open.push(tag); line += tag }
    else if (close) { open.pop(); line += close }
    else if (newline) {
      out.push(line + '</span>'.repeat(open.length))
      line = open.join('')
    } else line += text ?? ''
  }
  out.push(line + '</span>'.repeat(open.length))
  return out
}

export { escape }
