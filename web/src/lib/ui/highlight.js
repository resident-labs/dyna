// Syntax highlighting for the file viewer, loaded on first use so the main
// bundle stays small. highlight.js core with the languages agents touch
// most; anything else shows as plain text.

let ready = null

function load() {
  ready ??= (async () => {
    const [{ default: hljs }, ...langs] = await Promise.all([
      import('highlight.js/lib/core'),
      import('highlight.js/lib/languages/go'),
      import('highlight.js/lib/languages/javascript'),
      import('highlight.js/lib/languages/typescript'),
      import('highlight.js/lib/languages/json'),
      import('highlight.js/lib/languages/css'),
      import('highlight.js/lib/languages/xml'),
      import('highlight.js/lib/languages/markdown'),
      import('highlight.js/lib/languages/bash'),
      import('highlight.js/lib/languages/python'),
      import('highlight.js/lib/languages/yaml'),
      import('highlight.js/lib/languages/rust'),
      import('highlight.js/lib/languages/ini'),
      import('highlight.js/lib/languages/sql'),
      import('highlight.js/lib/languages/diff'),
      import('highlight.js/lib/languages/dockerfile'),
      import('highlight.js/lib/languages/makefile'),
      import('highlight.js/lib/languages/c'),
      import('highlight.js/lib/languages/cpp'),
      import('highlight.js/lib/languages/java'),
      import('highlight.js/lib/languages/ruby'),
      import('highlight.js/lib/languages/php'),
      import('highlight.js/lib/languages/swift'),
      import('highlight.js/lib/languages/kotlin'),
      import('highlight.js/lib/languages/lua'),
    ])
    const names = ['go', 'javascript', 'typescript', 'json', 'css', 'xml', 'markdown', 'bash', 'python', 'yaml', 'rust', 'ini', 'sql', 'diff', 'dockerfile', 'makefile', 'c', 'cpp', 'java', 'ruby', 'php', 'swift', 'kotlin', 'lua']
    names.forEach((n, i) => hljs.registerLanguage(n, langs[i].default))
    return hljs
  })()
  return ready
}

// File extensions and names to highlight.js languages. The server's
// language field wins when it names one we have.
const EXT = {
  go: 'go', mod: 'go', js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'javascript',
  ts: 'typescript', tsx: 'typescript', json: 'json', jsonc: 'json', css: 'css', scss: 'css',
  html: 'xml', htm: 'xml', xml: 'xml', svg: 'xml', svelte: 'xml', vue: 'xml',
  md: 'markdown', markdown: 'markdown', sh: 'bash', bash: 'bash', zsh: 'bash', fish: 'bash',
  py: 'python', yml: 'yaml', yaml: 'yaml', rs: 'rust', toml: 'ini', ini: 'ini', conf: 'ini', cfg: 'ini',
  sql: 'sql', diff: 'diff', patch: 'diff', c: 'c', h: 'c', cc: 'cpp', cpp: 'cpp', hpp: 'cpp',
  java: 'java', rb: 'ruby', php: 'php', swift: 'swift', kt: 'kotlin', lua: 'lua',
}
const NAMES = { dockerfile: 'dockerfile', makefile: 'makefile', 'go.sum': '', '.bashrc': 'bash', '.zshrc': 'bash' }

export function languageOf(name, hint = '') {
  const h = (hint || '').toLowerCase()
  const alias = { 'go.mod': 'go', html: 'xml', svelte: 'xml', shell: 'bash', sh: 'bash', toml: 'ini', js: 'javascript', ts: 'typescript' }
  if (h && (EXT[h] || alias[h] || Object.values(EXT).includes(h))) return alias[h] || EXT[h] || h
  const lower = name.toLowerCase()
  if (lower in NAMES) return NAMES[lower]
  const ext = lower.includes('.') ? lower.split('.').pop() : ''
  return EXT[ext] || ''
}

const escape = (s) => s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c])

// Highlighting a huge file blocks the page; past these limits it stays plain.
const MAX_CHARS = 300_000

// highlight resolves to safe HTML for text. Plain text comes back escaped.
export async function highlight(text, lang) {
  if (!lang || text.length > MAX_CHARS) return escape(text)
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
export function splitLines(html) {
  const out = []
  const open = [] // the opening tags in force
  let line = ''
  const re = /(<span[^>]*>)|(<\/span>)|(\n)|([^<\n]+|<)/g
  let m
  while ((m = re.exec(html))) {
    if (m[1]) { open.push(m[1]); line += m[1] }
    else if (m[2]) { open.pop(); line += m[2] }
    else if (m[3]) {
      out.push(line + '</span>'.repeat(open.length))
      line = open.join('')
    } else line += m[4]
  }
  out.push(line + '</span>'.repeat(open.length))
  return out
}

export { escape }
