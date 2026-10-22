// Plain computer activity text shared by the pill and compact DM rows.
import { appName } from './computers.js'

export const PILL_MAX = 48

// clip cuts a line at a word edge and ends it with an ellipsis.
export function clip(text, max = PILL_MAX) {
  const t = String(text || '').replace(/\s+/g, ' ').trim()
  if (t.length <= max) return t
  const cut = t.slice(0, max - 1)
  const edge = cut.lastIndexOf(' ')
  return (edge > max * 0.5 ? cut.slice(0, edge) : cut).replace(/[\s,;:·-]+$/, '') + '…'
}

// keyName: "ctrl+a" reads "Ctrl+A". One letter goes up; a word gets a capital.
const keyName = (k) => k.split('+').map((p) => (p.length === 1 ? p.toUpperCase() : p[0].toUpperCase() + p.slice(1).toLowerCase())).join('+')

export const TYPED_MAX = 24

// actPhrase turns an act line such as "click · 120, 340", "scroll · down · 5, 9",
// "keypress · ctrl+a", "type · "SAVE10"" or "open · Chromium" into plain words
// with its object. app is the short name of the front window, for a click.
// A secret reads "Typed a password"; text the daemon did not send reads "Typed 6 characters". Positions are left out.
const CLICKS = { click: 'Clicked', 'double click': 'Double-clicked', 'right click': 'Right-clicked', 'middle click': 'Middle-clicked' }
const PAST = { move: 'Moved the pointer', drag: 'Dragged', wait: 'Waited for the screen', screenshot: 'Looked at the screen' }
export function actPhrase(line, app = '') {
  const [verb, rest = ''] = String(line || '').split(' · ')
  const v = verb.trim().toLowerCase()
  const arg = rest.trim()
  if (CLICKS[v]) return app ? `${CLICKS[v]} in ${app}` : CLICKS[v]
  if (v === 'keypress') return arg ? `Pressed ${keyName(arg)}` : 'Pressed a key'
  if (v === 'scroll') {
    const dir = /^(down|up|left|right)$/i.test(arg) ? arg.toLowerCase() : ''
    return dir ? `Scrolled ${dir}` : 'Scrolled'
  }
  if (v === 'type') {
    if (arg === '(secret)') return 'Typed a password'
    const n = arg.match(/^(\d+) characters?$/)?.[1]
    if (n) return `Typed ${n} character${n === '1' ? '' : 's'}`
    const text = arg.replace(/^"(.*)"$/, '$1')
    // The daemon cuts the text at 24 characters and adds an ellipsis.
    return !text || text === '(hidden)' ? 'Typed text' : `Typed "${clip(text, TYPED_MAX + 1)}"`
  }
  if (v === 'open') return arg ? `Opened ${arg}` : 'Opened an app'
  return PAST[v] ?? ''
}

// windowPhrase: "In Chromium · Ivan Moskvitin". Empty with no window.
export function windowPhrase(c) {
  const a = c?.apps?.[0]
  const app = appName(a?.appId)
  if (!app && !a?.title) return ''
  return `In ${[app, a.title].filter(Boolean).join(' · ')}`
}

// The saved intent stays visible until another action replaces it.
// Keep the full text here; the pill uses CSS ellipsis and a full hover title.
export function computerLine({ c = null } = {}) {
  return c?.latestStatus?.text?.trim() || windowPhrase(c) || 'No window yet'
}
