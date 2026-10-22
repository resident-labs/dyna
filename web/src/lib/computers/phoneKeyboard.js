// A real, visually hidden textarea opens the phone keyboard. Keep one sentinel
// character so mobile keyboards can report Backspace even after we clear text.
const sentinel = '\u200b'
const symbols = { esc: 0xff1b, tab: 0xff09, left: 0xff51, up: 0xff52, right: 0xff53, down: 0xff54 }
const special = { Escape: 'esc', Tab: 'tab', ArrowLeft: 'left', ArrowUp: 'up', ArrowRight: 'right', ArrowDown: 'down', Enter: 0xff0d, Backspace: 0xff08, Delete: 0xffff, Home: 0xff50, End: 0xff57, PageUp: 0xff55, PageDown: 0xff56 }

export function phoneKeyboard(node, { sendKey, changed = () => {} }) {
  let composing = false
  let ctrl = false, alt = false
  const reset = () => { node.value = sentinel; node.setSelectionRange(1, 1) }
  const modifiers = () => changed({ ctrl, alt })
  function send(symbol) {
    if (ctrl) sendKey(0xffe3, 'ControlLeft', true)
    if (alt) sendKey(0xffe9, 'AltLeft', true)
    sendKey(symbol)
    if (alt) sendKey(0xffe9, 'AltLeft', false)
    if (ctrl) sendKey(0xffe3, 'ControlLeft', false)
    ctrl = alt = false
    modifiers()
  }
  function text(value) {
    for (const char of value.replace(/\r\n?/g, '\n')) {
      const cp = char.codePointAt(0)
      send(char === '\n' ? 0xff0d : char === '\t' ? 0xff09 : cp <= 0xff ? cp : 0x1000000 + cp)
    }
  }
  function input(e) {
    if (composing || e.isComposing) return
    if (e.inputType?.startsWith('delete')) send(e.inputType.endsWith('Forward') ? 0xffff : 0xff08)
    else text(node.value.startsWith(sentinel) ? node.value.slice(1) : node.value)
    reset()
  }
  function end(e) {
    composing = false
    text(e.data ?? node.value.replace(sentinel, ''))
    reset()
  }
  function paste(e) {
    e.preventDefault()
    text(e.clipboardData.getData('text/plain'))
    reset()
  }
  function key(name) {
    if (name === 'ctrl' || name === 'alt') {
      if (name === 'ctrl') ctrl = !ctrl
      else alt = !alt
      modifiers()
    } else send(symbols[name] ?? name)
  }
  function keydown(e) {
    if (composing || e.isComposing || e.keyCode === 229) return
    if (special[e.key] !== undefined) {
      e.preventDefault()
      key(special[e.key])
    }
  }
  const events = { input, compositionstart: () => { composing = true }, compositionend: end, paste, keydown }
  for (const [type, fn] of Object.entries(events)) node.addEventListener(type, fn)
  reset()
  modifiers()
  return {
    key,
    destroy() { for (const [type, fn] of Object.entries(events)) node.removeEventListener(type, fn) },
  }
}
