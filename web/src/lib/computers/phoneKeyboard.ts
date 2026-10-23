// A real, visually hidden textarea opens the phone keyboard. Keep one sentinel
// character so mobile keyboards can report Backspace even after we clear text.
const sentinel = '\u200b'

type Named = 'esc' | 'tab' | 'left' | 'up' | 'right' | 'down'
const symbols: Record<Named, number> = { esc: 0xff1b, tab: 0xff09, left: 0xff51, up: 0xff52, right: 0xff53, down: 0xff54 }
// special maps a KeyboardEvent.key to a named key or an X11 keysym.
const special: Record<string, Named | number> = { Escape: 'esc', Tab: 'tab', ArrowLeft: 'left', ArrowUp: 'up', ArrowRight: 'right', ArrowDown: 'down', Enter: 0xff0d, Backspace: 0xff08, Delete: 0xffff, Home: 0xff50, End: 0xff57, PageUp: 0xff55, PageDown: 0xff56 }

export interface Modifiers { ctrl: boolean, alt: boolean }

export interface PhoneKeyboardOptions {
  sendKey: (symbol: number, code?: string, down?: boolean) => void
  changed?: (state: Modifiers) => void
}

// inputType and isComposing read InputEvent fields from an Event, because some keyboards send a plain Event.
const inputType = (e: Event): string => ('inputType' in e && typeof e.inputType === 'string' ? e.inputType : '')
const isComposing = (e: Event): boolean => 'isComposing' in e && e.isComposing === true

export function phoneKeyboard(node: HTMLTextAreaElement, { sendKey, changed = () => {} }: PhoneKeyboardOptions) {
  let composing = false
  let ctrl = false, alt = false
  const reset = () => { node.value = sentinel; node.setSelectionRange(1, 1) }
  const modifiers = () => changed({ ctrl, alt })
  function send(symbol: number) {
    if (ctrl) sendKey(0xffe3, 'ControlLeft', true)
    if (alt) sendKey(0xffe9, 'AltLeft', true)
    sendKey(symbol)
    if (alt) sendKey(0xffe9, 'AltLeft', false)
    if (ctrl) sendKey(0xffe3, 'ControlLeft', false)
    ctrl = alt = false
    modifiers()
  }
  function text(value: string) {
    for (const char of value.replace(/\r\n?/g, '\n')) {
      const cp = char.codePointAt(0) ?? 0
      send(char === '\n' ? 0xff0d : char === '\t' ? 0xff09 : cp <= 0xff ? cp : 0x1000000 + cp)
    }
  }
  function input(e: Event) {
    if (composing || isComposing(e)) return
    const type = inputType(e)
    if (type.startsWith('delete')) send(type.endsWith('Forward') ? 0xffff : 0xff08)
    else text(node.value.startsWith(sentinel) ? node.value.slice(1) : node.value)
    reset()
  }
  function end(e: CompositionEvent) {
    composing = false
    text(e.data ?? node.value.replace(sentinel, ''))
    reset()
  }
  function paste(e: ClipboardEvent) {
    e.preventDefault()
    const data = e.clipboardData
    if (!data) throw new TypeError("Cannot read properties of null (reading 'getData')")
    text(data.getData('text/plain'))
    reset()
  }
  function key(name: 'ctrl' | 'alt' | Named | number) {
    if (name === 'ctrl' || name === 'alt') {
      if (name === 'ctrl') ctrl = !ctrl
      else alt = !alt
      modifiers()
    } else send(typeof name === 'number' ? name : symbols[name])
  }
  function keydown(e: KeyboardEvent) {
    if (composing || e.isComposing || e.keyCode === 229) return
    const name = special[e.key]
    if (name !== undefined) {
      e.preventDefault()
      key(name)
    }
  }
  const compositionstart = () => { composing = true }
  node.addEventListener('input', input)
  node.addEventListener('compositionstart', compositionstart)
  node.addEventListener('compositionend', end)
  node.addEventListener('paste', paste)
  node.addEventListener('keydown', keydown)
  reset()
  modifiers()
  return {
    key,
    destroy() {
      node.removeEventListener('input', input)
      node.removeEventListener('compositionstart', compositionstart)
      node.removeEventListener('compositionend', end)
      node.removeEventListener('paste', paste)
      node.removeEventListener('keydown', keydown)
    },
  }
}
